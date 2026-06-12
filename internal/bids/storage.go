package bids

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"molotlite/internal/platform/postgres"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations returns the feature's goose set; main applies it at boot.
func Migrations() postgres.Migrations {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(fmt.Sprintf("bids: migrations subtree: %v", err))
	}
	return postgres.Migrations{FS: sub, VersionTable: "goose_version_bids"}
}

// insertBid stores the bid inside the caller's transaction and returns
// the database-assigned creation time.
func insertBid(ctx context.Context, tx pgx.Tx, b Bid) (time.Time, error) {
	var createdAt time.Time
	err := tx.QueryRow(ctx,
		`INSERT INTO bids (id, lot_id, bidder_id, amount_minor)
		 VALUES ($1, $2, $3, $4)
		 RETURNING created_at`,
		b.ID, b.LotID, b.BidderID, b.AmountMinor,
	).Scan(&createdAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("insert bid: %w", err)
	}
	return createdAt, nil
}

// topBidder returns the holder of the highest existing bid on the lot,
// or uuid.Nil when there is none. Amounts strictly increase per lot, so
// the highest amount is also the latest bid.
func topBidder(ctx context.Context, tx pgx.Tx, lotID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT bidder_id FROM bids WHERE lot_id = $1 ORDER BY amount_minor DESC LIMIT 1`,
		lotID,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("select top bidder: %w", err)
	}
	return id, nil
}

func countBids(ctx context.Context, pool *pgxpool.Pool, lotID uuid.UUID) (int, error) {
	var total int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM bids WHERE lot_id = $1`, lotID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count bids: %w", err)
	}
	return total, nil
}

func listBids(ctx context.Context, pool *pgxpool.Pool, lotID uuid.UUID, limit, offset int) ([]Bid, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, lot_id, bidder_id, amount_minor, created_at
		 FROM bids
		 WHERE lot_id = $1
		 ORDER BY created_at DESC, amount_minor DESC
		 LIMIT $2 OFFSET $3`,
		lotID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list bids: %w", err)
	}
	defer rows.Close()

	items := make([]Bid, 0, limit)
	for rows.Next() {
		var b Bid
		if err := rows.Scan(&b.ID, &b.LotID, &b.BidderID, &b.AmountMinor, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan bid: %w", err)
		}
		items = append(items, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bids: %w", err)
	}
	return items, nil
}
