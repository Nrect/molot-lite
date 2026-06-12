// Package bids owns bid placement and history: the bids table plus the
// one genuinely concurrent write path of the service. The lot's price
// state belongs to the lots feature, so bids moves it only through
// lots.PlaceBidTx inside a transaction it owns (rule 2: a cross-feature
// need is a plain call into the owner's service function, never a query
// against the owner's table).
package bids

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"molotlite/internal/lots"
	"molotlite/internal/notify"
	"molotlite/internal/platform/errs"
	"molotlite/internal/platform/postgres"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// Service implements the bids feature as transaction scripts (rule 3).
type Service struct {
	pool   *pgxpool.Pool
	lots   *lots.Service
	notify *notify.Service
}

func NewService(pool *pgxpool.Pool, lotsSvc *lots.Service, notifySvc *notify.Service) *Service {
	return &Service{pool: pool, lots: lotsSvc, notify: notifySvc}
}

// Bid is a bid as clients see it.
type Bid struct {
	ID          uuid.UUID `json:"id"`
	LotID       uuid.UUID `json:"lotId"`
	BidderID    uuid.UUID `json:"bidderId"`
	AmountMinor int64     `json:"amountMinor"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Place puts a bid on a lot. Everything that must be atomic happens in
// one transaction: lots.PlaceBidTx locks the lot row (FOR UPDATE),
// validates the bid against the current price and moves it, then this
// feature inserts the bid into its own table under the same tx. The row
// lock serializes concurrent bids on one lot, so of N simultaneous bids
// with the same amount exactly one can win.
func (s *Service) Place(ctx context.Context, lotID, bidderID uuid.UUID, amountMinor int64) (Bid, error) {
	if amountMinor <= 0 {
		return Bid{}, errs.BadRequest("invalid-amount")
	}

	bid := Bid{ID: uuid.New(), LotID: lotID, BidderID: bidderID, AmountMinor: amountMinor}
	var (
		outbid   uuid.UUID // previous top bidder; uuid.Nil when none
		lotTitle string
	)
	err := postgres.RunInTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		placement, err := s.lots.PlaceBidTx(ctx, tx, lotID, bidderID, amountMinor)
		if err != nil {
			return err
		}
		lotTitle = placement.Title

		// The previous top bidder comes from this feature's own table,
		// read under the same lot lock, before the new bid lands.
		outbid, err = topBidder(ctx, tx, lotID)
		if err != nil {
			return err
		}

		bid.CreatedAt, err = insertBid(ctx, tx, bid)
		return err
	})
	if err != nil {
		return Bid{}, err
	}

	if outbid != uuid.Nil && outbid != bidderID {
		// Notify the outbid user. In Molot this is an event published
		// through the outbox (maturation trigger #2: a second consumer
		// of the feature's data); for the MVP it is a direct best-effort
		// call into the notify feature (rule 2), made only after the
		// transaction committed so a notification can never precede —
		// or outlive — a rolled-back bid.
		s.notify.Outbid(ctx, notify.OutbidEvent{
			LotID:          lotID,
			LotTitle:       lotTitle,
			OutbidUserID:   outbid,
			NewAmountMinor: amountMinor,
		})
	}
	return bid, nil
}

// HistoryResult is one page of a lot's bid history, newest first.
type HistoryResult struct {
	Items []Bid `json:"items"`
	Total int   `json:"total"`
	Page  int   `json:"page"`
}

// History returns the lot's bids, newest first. A lot without bids (or
// an unknown lot id) yields an empty page.
func (s *Service) History(ctx context.Context, lotID uuid.UUID, page, pageSize int) (HistoryResult, error) {
	switch {
	case page == 0:
		page = 1
	case page < 1:
		return HistoryResult{}, errs.BadRequest("invalid-page")
	}
	switch {
	case pageSize == 0:
		pageSize = defaultPageSize
	case pageSize < 1 || pageSize > maxPageSize:
		return HistoryResult{}, errs.BadRequest("invalid-page-size")
	}

	total, err := countBids(ctx, s.pool, lotID)
	if err != nil {
		return HistoryResult{}, err
	}
	items, err := listBids(ctx, s.pool, lotID, pageSize, (page-1)*pageSize)
	if err != nil {
		return HistoryResult{}, err
	}
	return HistoryResult{Items: items, Total: total, Page: page}, nil
}
