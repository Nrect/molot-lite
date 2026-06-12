// Package testutil is the shared infrastructure of feature integration
// tests: the TEST_DATABASE_URL gate, a connected pool, migration
// application safe under parallel test packages, and a bare JSON HTTP
// helper. Platform discipline applies here as in internal/platform
// (rule 7): no business types, no feature SQL — every feature assembles
// its own test application.
package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"molotlite/internal/platform/postgres"
)

// JWTSecret is the HS256 key test applications sign and verify with
// (32 bytes, the platform minimum).
const JWTSecret = "0123456789abcdef0123456789abcdef"

// migrateLockKey is an arbitrary advisory-lock id shared by all test
// packages, so concurrent `go test ./...` runs serialize migrations.
const migrateLockKey = 752_310_001

// DSN returns TEST_DATABASE_URL or skips the test: integration tests
// run only against the real Postgres from docker-compose.
func DSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set; run `make up` and export it to run integration tests")
	}
	return dsn
}

// Pool opens a pgx pool on the test database and closes it with the
// test.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.New(context.Background(), DSN(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// Migrate applies the given migration sets under a Postgres advisory
// lock: test packages run in parallel and share migration sets, and
// goose alone does not guard against two providers applying the same
// set at once.
func Migrate(t *testing.T, pool *pgxpool.Pool, sets ...postgres.Migrations) {
	t.Helper()
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()

	_, err = conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey)
	require.NoError(t, err)
	defer func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrateLockKey)
	}()

	require.NoError(t, postgres.RunMigrations(ctx, DSN(t), sets))
}

// DoJSON sends a JSON request (token optional) and returns the response
// together with its decoded body; an empty body decodes to nil.
func DoJSON(t *testing.T, method, url, token string, body any) (*http.Response, map[string]any) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_ = resp.Body.Close()

	var decoded map[string]any
	if len(data) > 0 {
		require.NoError(t, json.Unmarshal(data, &decoded), "response body: %s", data)
	}
	return resp, decoded
}
