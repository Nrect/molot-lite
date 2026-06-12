package users

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"molotlite/internal/platform/errs"
	"molotlite/internal/platform/postgres"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrations returns the feature's goose set; main applies it at boot.
func Migrations() postgres.Migrations {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		// The embedded path is a constant; failing here is a
		// programming error, not a runtime condition.
		panic(fmt.Sprintf("users: migrations subtree: %v", err))
	}
	return postgres.Migrations{FS: sub, VersionTable: "goose_version_users"}
}

// uniqueViolation is the Postgres error code for a violated unique
// constraint (here: the email index).
const uniqueViolation = "23505"

// userRow mirrors the users table. The password hash lives only here;
// the public User type has no field for it, so it cannot leak into a
// response by construction.
type userRow struct {
	id           uuid.UUID
	email        string
	passwordHash string
	displayName  string
	createdAt    time.Time
}

func insertUser(ctx context.Context, pool *pgxpool.Pool, row userRow) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, $3, $4)`,
		row.id, row.email, row.passwordHash, row.displayName,
	)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return errs.Conflict("email-taken").WithCause(err)
	}
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func getUserByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (userRow, error) {
	var row userRow
	err := pool.QueryRow(ctx,
		`SELECT id, email, password_hash, display_name, created_at FROM users WHERE email = $1`,
		email,
	).Scan(&row.id, &row.email, &row.passwordHash, &row.displayName, &row.createdAt)
	if err != nil {
		return userRow{}, fmt.Errorf("select user by email: %w", err)
	}
	return row, nil
}

func getUserByID(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (userRow, error) {
	var row userRow
	err := pool.QueryRow(ctx,
		`SELECT id, email, password_hash, display_name, created_at FROM users WHERE id = $1`,
		id,
	).Scan(&row.id, &row.email, &row.passwordHash, &row.displayName, &row.createdAt)
	if err != nil {
		return userRow{}, fmt.Errorf("select user by id: %w", err)
	}
	return row, nil
}

func userExists(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("select user exists: %w", err)
	}
	return exists, nil
}
