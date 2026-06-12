// Package postgres owns the pgx connection pool, the transaction idiom
// of the codebase (RunInTx with a deferred finish that never loses a
// rollback error), and goose migrations. Application code uses pgx
// natively; the database/sql adapter appears only inside RunMigrations,
// because goose requires *sql.DB — that connection is opened for the
// duration of the migrations and closed before the server starts.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	// Registers the "pgx" database/sql driver used by RunMigrations.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// New opens the process connection pool and verifies connectivity with
// a ping bound to ctx. Pool sizing comes from the DSN (pool_max_conns,
// ...); pgxpool defaults are fine for an MVP.
func New(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// RunInTx executes fn inside a transaction: commit when fn returns nil,
// rollback otherwise. The deferred finish sees the named return error,
// so fn can simply return — no manual commit/rollback in call sites. A
// failed rollback is joined with the original error, never lost.
func RunInTx(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		err = finishTx(ctx, tx, err)
	}()

	return fn(ctx, tx)
}

func finishTx(ctx context.Context, tx pgx.Tx, err error) error {
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("rollback tx: %w", rbErr))
		}
		return err
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return fmt.Errorf("commit tx: %w", commitErr)
	}
	return nil
}

// Migrations is one feature's goose migration set. FS must contain
// *.sql goose files at its root: embed "migrations/*.sql" in the
// feature package and pass fs.Sub(embedded, "migrations"). Each feature
// gets its own VersionTable (e.g. "goose_version_users"), so features
// version independently and can be deleted without touching a shared
// history.
type Migrations struct {
	FS           fs.FS
	VersionTable string
}

// RunMigrations applies every migration set in order over a short-lived
// database/sql connection (goose's requirement) opened from dsn and
// closed before returning. Call it once at startup, before the server
// accepts traffic.
func RunMigrations(ctx context.Context, dsn string, sets []Migrations) error {
	if len(sets) == 0 {
		return nil
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	defer func() { _ = db.Close() }()

	for _, set := range sets {
		if err := applyMigrations(ctx, db, set); err != nil {
			return err
		}
	}
	return nil
}

func applyMigrations(ctx context.Context, db *sql.DB, set Migrations) error {
	store, err := database.NewStore(database.DialectPostgres, set.VersionTable)
	if err != nil {
		return fmt.Errorf("migration store %q: %w", set.VersionTable, err)
	}

	provider, err := goose.NewProvider(goose.DialectCustom, db, set.FS, goose.WithStore(store))
	if err != nil {
		return fmt.Errorf("migration provider %q: %w", set.VersionTable, err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations %q: %w", set.VersionTable, err)
	}
	return nil
}
