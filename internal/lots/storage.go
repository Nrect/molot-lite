package lots

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"strings"

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
		panic(fmt.Sprintf("lots: migrations subtree: %v", err))
	}
	return postgres.Migrations{FS: sub, VersionTable: "goose_version_lots"}
}

const lotColumns = `id, owner_id, title, description, start_price_minor, currency,
	current_price_minor, bid_count, ends_at, created_at`

func scanLot(row pgx.Row) (Lot, error) {
	var l Lot
	err := row.Scan(&l.ID, &l.OwnerID, &l.Title, &l.Description, &l.StartPriceMinor,
		&l.Currency, &l.CurrentPriceMinor, &l.BidCount, &l.EndsAt, &l.CreatedAt)
	return l, err
}

func insertLot(ctx context.Context, pool *pgxpool.Pool, l Lot) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO lots (id, owner_id, title, description, start_price_minor, currency,
			current_price_minor, ends_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		l.ID, l.OwnerID, l.Title, l.Description, l.StartPriceMinor, l.Currency,
		l.CurrentPriceMinor, l.EndsAt,
	)
	if err != nil {
		return fmt.Errorf("insert lot: %w", err)
	}
	return nil
}

func getLot(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (Lot, error) {
	l, err := scanLot(pool.QueryRow(ctx, `SELECT `+lotColumns+` FROM lots WHERE id = $1`, id))
	if err != nil {
		return Lot{}, fmt.Errorf("select lot: %w", err)
	}
	return l, nil
}

// getLotForUpdate locks the lot row for the rest of the transaction and
// reports whether the lot is already closed (by the database clock, the
// single time source for lot activity).
func getLotForUpdate(ctx context.Context, tx pgx.Tx, id uuid.UUID) (lot Lot, closed bool, err error) {
	err = tx.QueryRow(ctx,
		`SELECT `+lotColumns+`, ends_at <= now() FROM lots WHERE id = $1 FOR UPDATE`, id,
	).Scan(&lot.ID, &lot.OwnerID, &lot.Title, &lot.Description, &lot.StartPriceMinor,
		&lot.Currency, &lot.CurrentPriceMinor, &lot.BidCount, &lot.EndsAt, &lot.CreatedAt, &closed)
	if err != nil {
		return Lot{}, false, fmt.Errorf("select lot for update: %w", err)
	}
	return lot, closed, nil
}

func updateLot(ctx context.Context, tx pgx.Tx, l Lot) error {
	_, err := tx.Exec(ctx,
		`UPDATE lots SET title = $2, description = $3, start_price_minor = $4,
			current_price_minor = $5, ends_at = $6
		 WHERE id = $1`,
		l.ID, l.Title, l.Description, l.StartPriceMinor, l.CurrentPriceMinor, l.EndsAt,
	)
	if err != nil {
		return fmt.Errorf("update lot: %w", err)
	}
	return nil
}

func deleteLot(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM lots WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete lot: %w", err)
	}
	return nil
}

// applyBid moves the price and bumps the bid counter; the caller holds
// the row lock from getLotForUpdate.
func applyBid(ctx context.Context, tx pgx.Tx, id uuid.UUID, amountMinor int64) error {
	_, err := tx.Exec(ctx,
		`UPDATE lots SET current_price_minor = $2, bid_count = bid_count + 1 WHERE id = $1`,
		id, amountMinor,
	)
	if err != nil {
		return fmt.Errorf("apply bid to lot: %w", err)
	}
	return nil
}

func countActiveLots(ctx context.Context, pool *pgxpool.Pool, q string) (int, error) {
	sql := `SELECT count(*) FROM lots WHERE ends_at > now()`
	var args []any
	if q != "" {
		sql += ` AND title ILIKE $1 ESCAPE '\'`
		args = append(args, likePattern(q))
	}

	var total int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count active lots: %w", err)
	}
	return total, nil
}

func listActiveLots(ctx context.Context, pool *pgxpool.Pool, q string, limit, offset int) ([]Lot, error) {
	sql := `SELECT ` + lotColumns + ` FROM lots WHERE ends_at > now()`
	var args []any
	if q != "" {
		sql += ` AND title ILIKE $1 ESCAPE '\'`
		args = append(args, likePattern(q))
	}
	sql += fmt.Sprintf(` ORDER BY ends_at LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list active lots: %w", err)
	}
	defer rows.Close()

	items := make([]Lot, 0, limit)
	for rows.Next() {
		l, err := scanLot(rows)
		if err != nil {
			return nil, fmt.Errorf("scan lot: %w", err)
		}
		items = append(items, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list active lots: %w", err)
	}
	return items, nil
}

// likePattern wraps the user's search term for ILIKE, escaping the
// pattern metacharacters so they match literally.
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}
