// Integration tests: they need a real Postgres and skip themselves when
// TEST_DATABASE_URL is not set (make up && make test-integration).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; run `make up` and export it to run integration tests")
	}

	pool, err := New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func TestRunInTx_Commit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS tx_commit_smoke (id int)")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS tx_commit_smoke")
	})

	err = RunInTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO tx_commit_smoke (id) VALUES (1)")
		return err
	})
	require.NoError(t, err)

	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM tx_commit_smoke").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestRunInTx_RollbackOnError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS tx_rollback_smoke (id int)")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS tx_rollback_smoke")
	})

	boom := errors.New("boom")
	err = RunInTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO tx_rollback_smoke (id) VALUES (1)"); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)

	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM tx_rollback_smoke").Scan(&count))
	assert.Equal(t, 0, count, "insert must be rolled back")
}

func TestRunMigrations(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dsn := os.Getenv("TEST_DATABASE_URL")

	const versionTable = "goose_version_migration_smoke"
	migrations := fstest.MapFS{
		"00001_create.sql": &fstest.MapFile{Data: []byte(
			"-- +goose Up\nCREATE TABLE migration_smoke (id int);\n-- +goose Down\nDROP TABLE migration_smoke;\n",
		)},
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS migration_smoke")
		_, _ = pool.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", versionTable))
	})

	sets := []Migrations{{FS: migrations, VersionTable: versionTable}}
	require.NoError(t, RunMigrations(ctx, dsn, sets))
	require.NoError(t, RunMigrations(ctx, dsn, sets), "second run must be a no-op")

	var exists bool
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'migration_smoke')",
	).Scan(&exists))
	assert.True(t, exists, "migrated table must exist")

	require.NoError(t, pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)", versionTable,
	).Scan(&exists))
	assert.True(t, exists, "per-feature version table must exist")
}

func TestRunMigrations_EmptySetIsNoOp(t *testing.T) {
	require.NoError(t, RunMigrations(context.Background(), "postgres://invalid-host/never-dialed", nil))
}
