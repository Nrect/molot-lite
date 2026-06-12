// Package lots owns the auction lots: CRUD over the lots table plus the
// price/bid-count state a bid moves. Cross-feature traffic follows
// rule 2 in both directions: lots asks the users feature about owners
// through users.UserExists, and the bids feature moves lot state only
// through PlaceBidTx — neither side ever queries the other's table.
package lots

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"molotlite/internal/platform/errs"
	"molotlite/internal/platform/postgres"
	"molotlite/internal/users"
)

const (
	maxTitleRunes   = 200
	defaultPageSize = 20
	maxPageSize     = 100
)

// Service implements the lots feature as transaction scripts (rule 3).
type Service struct {
	pool  *pgxpool.Pool
	users *users.Service
}

func NewService(pool *pgxpool.Pool, usersSvc *users.Service) *Service {
	return &Service{pool: pool, users: usersSvc}
}

// Lot is the lot as clients see it; it mirrors the table one to one.
type Lot struct {
	ID                uuid.UUID `json:"id"`
	OwnerID           uuid.UUID `json:"ownerId"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	StartPriceMinor   int64     `json:"startPriceMinor"`
	Currency          string    `json:"currency"`
	CurrentPriceMinor int64     `json:"currentPriceMinor"`
	BidCount          int       `json:"bidCount"`
	EndsAt            time.Time `json:"endsAt"`
	CreatedAt         time.Time `json:"createdAt"`
}

// CreateInput is what a new lot needs from the client.
type CreateInput struct {
	Title           string
	Description     string
	StartPriceMinor int64
	Currency        string
	EndsAt          time.Time
}

// Create validates the input and stores a new lot owned by ownerID.
func (s *Service) Create(ctx context.Context, ownerID uuid.UUID, in CreateInput) (uuid.UUID, error) {
	title, err := validTitle(in.Title)
	if err != nil {
		return uuid.Nil, err
	}
	currency, err := validCurrency(in.Currency)
	if err != nil {
		return uuid.Nil, err
	}
	if err := validPrice(in.StartPriceMinor); err != nil {
		return uuid.Nil, err
	}
	if err := validEndsAt(in.EndsAt); err != nil {
		return uuid.Nil, err
	}

	// Rule 2: the owner check is a plain call into the users feature,
	// not a query against its table (and not a cross-feature FK).
	exists, err := s.users.UserExists(ctx, ownerID)
	if err != nil {
		return uuid.Nil, err
	}
	if !exists {
		return uuid.Nil, errs.Unauthorized("unknown-user")
	}

	lot := Lot{
		ID:                uuid.New(),
		OwnerID:           ownerID,
		Title:             title,
		Description:       strings.TrimSpace(in.Description),
		StartPriceMinor:   in.StartPriceMinor,
		Currency:          currency,
		CurrentPriceMinor: in.StartPriceMinor, // no bids yet: current price is the start price
		EndsAt:            in.EndsAt,
	}
	if err := insertLot(ctx, s.pool, lot); err != nil {
		return uuid.Nil, err
	}
	return lot.ID, nil
}

// Get returns a lot by id, ended ones included (a closed auction is
// still viewable; only the listing filters to active).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Lot, error) {
	lot, err := getLot(ctx, s.pool, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lot{}, errs.NotFound("lot-not-found").WithCause(err)
	}
	if err != nil {
		return Lot{}, err
	}
	return lot, nil
}

// ListInput carries the listing query; zero Page/PageSize mean the
// defaults (1 and 20).
type ListInput struct {
	Page     int
	PageSize int
	Query    string
}

// ListResult is one page of active lots ordered by closing time.
type ListResult struct {
	Items []Lot `json:"items"`
	Total int   `json:"total"`
	Page  int   `json:"page"`
}

// List returns active lots (ends_at in the future), optionally filtered
// by a case-insensitive title search, soonest-closing first.
func (s *Service) List(ctx context.Context, in ListInput) (ListResult, error) {
	page, pageSize, err := validPage(in.Page, in.PageSize)
	if err != nil {
		return ListResult{}, err
	}

	total, err := countActiveLots(ctx, s.pool, in.Query)
	if err != nil {
		return ListResult{}, err
	}
	items, err := listActiveLots(ctx, s.pool, in.Query, pageSize, (page-1)*pageSize)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Items: items, Total: total, Page: page}, nil
}

// UpdateInput carries the PATCH body; nil fields stay unchanged.
type UpdateInput struct {
	Title           *string
	Description     *string
	StartPriceMinor *int64
	EndsAt          *time.Time
}

// Update edits a lot. Only the owner may edit, and only while the lot
// has no bids.
//
// The no-bids check reads bid_count from the locked lot row, not the
// bids feature: bid_count is this feature's own denormalized state,
// bumped by PlaceBidTx in the very transaction that inserts the bid, so
// under FOR UPDATE the check cannot race a concurrent bid. Asking bids
// instead would re-open that race outside the lock and invert the
// dependency (bids already calls into lots).
func (s *Service) Update(ctx context.Context, lotID, callerID uuid.UUID, in UpdateInput) (Lot, error) {
	var updated Lot
	err := postgres.RunInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		lot, _, err := lockOwnedLotWithoutBids(ctx, tx, lotID, callerID)
		if err != nil {
			return err
		}

		if in.Title != nil {
			title, err := validTitle(*in.Title)
			if err != nil {
				return err
			}
			lot.Title = title
		}
		if in.Description != nil {
			lot.Description = strings.TrimSpace(*in.Description)
		}
		if in.StartPriceMinor != nil {
			if err := validPrice(*in.StartPriceMinor); err != nil {
				return err
			}
			lot.StartPriceMinor = *in.StartPriceMinor
			lot.CurrentPriceMinor = *in.StartPriceMinor // no bids: current tracks start
		}
		if in.EndsAt != nil {
			if err := validEndsAt(*in.EndsAt); err != nil {
				return err
			}
			lot.EndsAt = *in.EndsAt
		}

		if err := updateLot(ctx, tx, lot); err != nil {
			return err
		}
		updated = lot
		return nil
	})
	if err != nil {
		return Lot{}, err
	}
	return updated, nil
}

// Delete removes a lot. Same guard as Update: owner only, no bids yet —
// which also means deletion can never orphan rows in the bids table.
func (s *Service) Delete(ctx context.Context, lotID, callerID uuid.UUID) error {
	return postgres.RunInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, err := lockOwnedLotWithoutBids(ctx, tx, lotID, callerID); err != nil {
			return err
		}
		return deleteLot(ctx, tx, lotID)
	})
}

// BidPlacement is what the bids feature gets back from a successful
// price move: identity for its own insert plus context for the outbid
// notification.
type BidPlacement struct {
	LotID              uuid.UUID
	Title              string
	PreviousPriceMinor int64
}

// PlaceBidTx applies a bid's effect on the lot inside the caller's
// transaction: it locks the lot row (FOR UPDATE), validates that the
// lot is open, that the bidder is not the owner and that the amount
// beats the current price, then moves current_price_minor and
// bid_count.
//
// This is the rule-2 pattern between bids and lots made explicit: bids
// never touches the lots table. It opens the transaction, calls this
// service function, and inserts into its own bids table under the same
// tx — so the price move and the bid row commit or roll back together,
// and the row lock taken here serializes concurrent bids on one lot.
func (s *Service) PlaceBidTx(ctx context.Context, tx pgx.Tx, lotID, bidderID uuid.UUID, amountMinor int64) (BidPlacement, error) {
	lot, closed, err := getLotForUpdate(ctx, tx, lotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return BidPlacement{}, errs.NotFound("lot-not-found").WithCause(err)
	}
	if err != nil {
		return BidPlacement{}, err
	}

	if closed {
		return BidPlacement{}, errs.Conflict("lot-closed")
	}
	if lot.OwnerID == bidderID {
		return BidPlacement{}, errs.Forbidden("owner-cannot-bid")
	}
	if amountMinor <= lot.CurrentPriceMinor {
		return BidPlacement{}, errs.Conflict("bid-too-low")
	}

	if err := applyBid(ctx, tx, lotID, amountMinor); err != nil {
		return BidPlacement{}, err
	}
	return BidPlacement{LotID: lotID, Title: lot.Title, PreviousPriceMinor: lot.CurrentPriceMinor}, nil
}

// lockOwnedLotWithoutBids is the shared guard of Update and Delete:
// lock the row, then not-found → 404, foreign lot → 403, bids placed →
// 409.
func lockOwnedLotWithoutBids(ctx context.Context, tx pgx.Tx, lotID, callerID uuid.UUID) (Lot, bool, error) {
	lot, closed, err := getLotForUpdate(ctx, tx, lotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lot{}, false, errs.NotFound("lot-not-found").WithCause(err)
	}
	if err != nil {
		return Lot{}, false, err
	}
	if lot.OwnerID != callerID {
		return Lot{}, false, errs.Forbidden("not-owner")
	}
	if lot.BidCount > 0 {
		return Lot{}, false, errs.Conflict("lot-has-bids")
	}
	return lot, closed, nil
}

func validTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if title == "" {
		return "", errs.BadRequest("title-required")
	}
	if utf8.RuneCountInString(title) > maxTitleRunes {
		return "", errs.BadRequest("title-too-long")
	}
	return title, nil
}

func validCurrency(raw string) (string, error) {
	if len(raw) != 3 {
		return "", errs.BadRequest("invalid-currency")
	}
	for _, c := range raw {
		if !('A' <= c && c <= 'Z') && !('a' <= c && c <= 'z') {
			return "", errs.BadRequest("invalid-currency")
		}
	}
	return strings.ToUpper(raw), nil
}

func validPrice(minor int64) error {
	if minor <= 0 {
		return errs.BadRequest("invalid-price")
	}
	return nil
}

func validEndsAt(ts time.Time) error {
	if !ts.After(time.Now()) {
		return errs.BadRequest("ends-at-not-future")
	}
	return nil
}

func validPage(page, pageSize int) (int, int, error) {
	switch {
	case page == 0:
		page = 1
	case page < 1:
		return 0, 0, errs.BadRequest("invalid-page")
	}
	switch {
	case pageSize == 0:
		pageSize = defaultPageSize
	case pageSize < 1 || pageSize > maxPageSize:
		return 0, 0, errs.BadRequest("invalid-page-size")
	}
	return page, pageSize, nil
}
