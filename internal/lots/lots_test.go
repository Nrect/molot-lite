// Integration tests over real HTTP and a real Postgres (rule 10). They
// skip themselves when TEST_DATABASE_URL is not set.
package lots_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"molotlite/internal/lots"
	"molotlite/internal/platform/auth"
	"molotlite/internal/testutil"
	"molotlite/internal/users"
)

// newTestApp assembles users (the lot owner dependency) and lots the
// same way main does.
func newTestApp(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.Migrate(t, pool, users.Migrations(), lots.Migrations())

	usersService := users.NewService(pool, testutil.JWTSecret, bcrypt.MinCost)
	usersHandler := users.NewHandler(usersService)
	lotsHandler := lots.NewHandler(lots.NewService(pool, usersService))

	r := chi.NewRouter()
	r.Route("/api", func(api chi.Router) {
		api.Group(func(public chi.Router) {
			usersHandler.RegisterPublic(public)
			lotsHandler.RegisterPublic(public)
		})
		api.Group(func(protected chi.Router) {
			protected.Use(auth.Middleware(testutil.JWTSecret))
			usersHandler.RegisterProtected(protected)
			lotsHandler.RegisterProtected(protected)
		})
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, pool
}

// newUser registers and logs in a fresh user, returning the token.
func newUser(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	email := uuid.NewString() + "@example.com"
	const password = "correct-horse-battery"

	resp, _ := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/users", "", map[string]string{
		"email": email, "password": password, "displayName": "Seller",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	resp, body := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/users/login", "", map[string]string{
		"email": email, "password": password,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	token, ok := body["token"].(string)
	require.True(t, ok)
	return token
}

func createLot(t *testing.T, srv *httptest.Server, token, title string, endsAt time.Time) string {
	t.Helper()
	resp, body := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/lots", token, map[string]any{
		"title":           title,
		"description":     "A test lot",
		"startPriceMinor": 10_000,
		"currency":        "USD",
		"endsAt":          endsAt.Format(time.RFC3339),
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "create lot: %v", body)
	id, ok := body["id"].(string)
	require.True(t, ok)
	return id
}

func TestCreateAndGet(t *testing.T) {
	srv, _ := newTestApp(t)
	token := newUser(t, srv)

	title := "Lot " + uuid.NewString()
	id := createLot(t, srv, token, title, time.Now().Add(24*time.Hour))

	resp, lot := testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/lots/"+id, "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, title, lot["title"])
	assert.Equal(t, "USD", lot["currency"])
	assert.EqualValues(t, 10_000, lot["startPriceMinor"])
	assert.EqualValues(t, 10_000, lot["currentPriceMinor"], "current price starts at start price")
	assert.EqualValues(t, 0, lot["bidCount"])
}

func TestCreate_RequiresToken(t *testing.T) {
	srv, _ := newTestApp(t)

	resp, body := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/lots", "", map[string]any{
		"title": "No token", "startPriceMinor": 100, "currency": "USD",
		"endsAt": time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "unauthorized", body["slug"])
}

func TestCreate_Validation(t *testing.T) {
	srv, _ := newTestApp(t)
	token := newUser(t, srv)
	future := time.Now().Add(time.Hour).Format(time.RFC3339)

	tests := []struct {
		name string
		req  map[string]any
		slug string
	}{
		{"empty title", map[string]any{"title": " ", "startPriceMinor": 100, "currency": "USD", "endsAt": future}, "title-required"},
		{"zero price", map[string]any{"title": "T", "startPriceMinor": 0, "currency": "USD", "endsAt": future}, "invalid-price"},
		{"bad currency", map[string]any{"title": "T", "startPriceMinor": 100, "currency": "DOLLARS", "endsAt": future}, "invalid-currency"},
		{"past endsAt", map[string]any{"title": "T", "startPriceMinor": 100, "currency": "USD",
			"endsAt": time.Now().Add(-time.Hour).Format(time.RFC3339)}, "ends-at-not-future"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/lots", token, tt.req)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.Equal(t, tt.slug, body["slug"])
		})
	}
}

func TestGet_UnknownLot(t *testing.T) {
	srv, _ := newTestApp(t)

	resp, body := testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/lots/"+uuid.NewString(), "", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "lot-not-found", body["slug"])
}

func TestList_FiltersAndPaginates(t *testing.T) {
	srv, pool := newTestApp(t)
	token := newUser(t, srv)

	// A unique marker isolates this test's lots from everything else
	// accumulated in the shared test database.
	marker := uuid.NewString()
	soon := createLot(t, srv, token, "A "+marker, time.Now().Add(1*time.Hour))
	later := createLot(t, srv, token, "B "+marker, time.Now().Add(2*time.Hour))
	ended := createLot(t, srv, token, "C "+marker, time.Now().Add(3*time.Hour))

	// Lots end when the clock passes ends_at; the test fast-forwards
	// time by editing the feature's own row instead of sleeping.
	_, err := pool.Exec(context.Background(),
		"UPDATE lots SET ends_at = now() - interval '1 minute' WHERE id = $1", ended)
	require.NoError(t, err)

	resp, body := testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/lots?q="+marker, "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, 2, body["total"], "ended lot must not be listed")
	items, ok := body["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)
	first, second := items[0].(map[string]any), items[1].(map[string]any)
	assert.Equal(t, soon, first["id"], "soonest-closing lot first")
	assert.Equal(t, later, second["id"])

	// Page 2 of size 1 holds the later lot.
	resp, body = testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/lots?q="+marker+"&page=2&pageSize=1", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, 2, body["total"])
	assert.EqualValues(t, 2, body["page"])
	items = body["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, later, items[0].(map[string]any)["id"])

	// pageSize is capped at 100.
	resp, body = testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/lots?pageSize=101", "", nil)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "invalid-page-size", body["slug"])
}

func TestPatch_Owner(t *testing.T) {
	srv, _ := newTestApp(t)
	token := newUser(t, srv)
	id := createLot(t, srv, token, "Old "+uuid.NewString(), time.Now().Add(time.Hour))

	resp, lot := testutil.DoJSON(t, http.MethodPatch, srv.URL+"/api/lots/"+id, token, map[string]any{
		"title":           "New title",
		"startPriceMinor": 25_000,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "New title", lot["title"])
	assert.EqualValues(t, 25_000, lot["startPriceMinor"])
	assert.EqualValues(t, 25_000, lot["currentPriceMinor"], "with no bids the current price follows the start price")
}

func TestPatch_NotOwner_Forbidden(t *testing.T) {
	srv, _ := newTestApp(t)
	owner := newUser(t, srv)
	stranger := newUser(t, srv)
	id := createLot(t, srv, owner, "Mine "+uuid.NewString(), time.Now().Add(time.Hour))

	resp, body := testutil.DoJSON(t, http.MethodPatch, srv.URL+"/api/lots/"+id, stranger, map[string]any{
		"title": "Hijacked",
	})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "not-owner", body["slug"])
}

func TestPatch_WithBids_Conflict(t *testing.T) {
	srv, pool := newTestApp(t)
	token := newUser(t, srv)
	id := createLot(t, srv, token, "Bid on "+uuid.NewString(), time.Now().Add(time.Hour))

	// Simulate a placed bid through the feature's own denormalized
	// counter; dragging the bids feature into lots tests would couple
	// the packages the rules keep apart.
	_, err := pool.Exec(context.Background(), "UPDATE lots SET bid_count = 1 WHERE id = $1", id)
	require.NoError(t, err)

	resp, body := testutil.DoJSON(t, http.MethodPatch, srv.URL+"/api/lots/"+id, token, map[string]any{
		"title": "Too late",
	})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "lot-has-bids", body["slug"])
}

func TestDelete(t *testing.T) {
	srv, _ := newTestApp(t)
	owner := newUser(t, srv)
	stranger := newUser(t, srv)
	id := createLot(t, srv, owner, "Doomed "+uuid.NewString(), time.Now().Add(time.Hour))

	resp, body := testutil.DoJSON(t, http.MethodDelete, srv.URL+"/api/lots/"+id, stranger, nil)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, "not-owner", body["slug"])

	resp, _ = testutil.DoJSON(t, http.MethodDelete, srv.URL+"/api/lots/"+id, owner, nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp, body = testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/lots/"+id, "", nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "lot-not-found", body["slug"])
}
