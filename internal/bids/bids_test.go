// Integration tests over real HTTP and a real Postgres (rule 10),
// including the bid race — the one genuinely concurrent path of the
// service. They skip themselves when TEST_DATABASE_URL is not set.
package bids_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"molotlite/internal/bids"
	"molotlite/internal/lots"
	"molotlite/internal/notify"
	"molotlite/internal/platform/auth"
	"molotlite/internal/testutil"
	"molotlite/internal/users"
)

// newTestApp assembles all three features the same way main does:
// bids → lots → users, wired by plain service calls.
func newTestApp(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.Migrate(t, pool, users.Migrations(), lots.Migrations(), bids.Migrations())

	usersService := users.NewService(pool, testutil.JWTSecret, bcrypt.MinCost)
	lotsService := lots.NewService(pool, usersService)
	usersHandler := users.NewHandler(usersService)
	lotsHandler := lots.NewHandler(lotsService)
	// Notifications run disabled (empty credentials): the tests assert
	// bidding behaviour, not Telegram delivery.
	bidsHandler := bids.NewHandler(bids.NewService(pool, lotsService, notify.NewService("", "")))

	r := chi.NewRouter()
	r.Route("/api", func(api chi.Router) {
		api.Group(func(public chi.Router) {
			usersHandler.RegisterPublic(public)
			lotsHandler.RegisterPublic(public)
			bidsHandler.RegisterPublic(public)
		})
		api.Group(func(protected chi.Router) {
			protected.Use(auth.Middleware(testutil.JWTSecret))
			usersHandler.RegisterProtected(protected)
			lotsHandler.RegisterProtected(protected)
			bidsHandler.RegisterProtected(protected)
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
		"email": email, "password": password, "displayName": "Bidder",
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

// createLot makes a lot starting at 10 000 minor units, open for a day.
func createLot(t *testing.T, srv *httptest.Server, token string) string {
	t.Helper()
	resp, body := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/lots", token, map[string]any{
		"title":           "Lot " + uuid.NewString(),
		"description":     "A contested lot",
		"startPriceMinor": 10_000,
		"currency":        "USD",
		"endsAt":          time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, "create lot: %v", body)
	id, ok := body["id"].(string)
	require.True(t, ok)
	return id
}

func placeBid(t *testing.T, srv *httptest.Server, token, lotID string, amount int64) (int, map[string]any) {
	t.Helper()
	resp, body := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/lots/"+lotID+"/bids", token,
		map[string]any{"amountMinor": amount})
	return resp.StatusCode, body
}

func getLot(t *testing.T, srv *httptest.Server, lotID string) map[string]any {
	t.Helper()
	resp, body := testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/lots/"+lotID, "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return body
}

func TestPlaceBid(t *testing.T) {
	srv, _ := newTestApp(t)
	owner := newUser(t, srv)
	bidder := newUser(t, srv)
	lotID := createLot(t, srv, owner)

	status, bid := placeBid(t, srv, bidder, lotID, 12_500)
	require.Equal(t, http.StatusCreated, status)
	assert.EqualValues(t, 12_500, bid["amountMinor"])
	assert.Equal(t, lotID, bid["lotId"])

	lot := getLot(t, srv, lotID)
	assert.EqualValues(t, 12_500, lot["currentPriceMinor"], "bid must move the lot price")
	assert.EqualValues(t, 1, lot["bidCount"])
}

func TestPlaceBid_OwnerForbidden(t *testing.T) {
	srv, _ := newTestApp(t)
	owner := newUser(t, srv)
	lotID := createLot(t, srv, owner)

	status, body := placeBid(t, srv, owner, lotID, 12_500)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "owner-cannot-bid", body["slug"])
}

func TestPlaceBid_TooLow(t *testing.T) {
	srv, _ := newTestApp(t)
	owner := newUser(t, srv)
	bidder := newUser(t, srv)
	lotID := createLot(t, srv, owner)

	// Equal to the current price is not enough: a bid must beat it.
	status, body := placeBid(t, srv, bidder, lotID, 10_000)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "bid-too-low", body["slug"])
}

func TestPlaceBid_ClosedLot(t *testing.T) {
	srv, pool := newTestApp(t)
	owner := newUser(t, srv)
	bidder := newUser(t, srv)
	lotID := createLot(t, srv, owner)

	// Lots close when the clock passes ends_at; the test fast-forwards
	// time by editing the row instead of sleeping through a real wait.
	_, err := pool.Exec(context.Background(),
		"UPDATE lots SET ends_at = now() - interval '1 minute' WHERE id = $1", lotID)
	require.NoError(t, err)

	status, body := placeBid(t, srv, bidder, lotID, 12_500)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "lot-closed", body["slug"])
}

func TestPlaceBid_UnknownLot(t *testing.T) {
	srv, _ := newTestApp(t)
	bidder := newUser(t, srv)

	status, body := placeBid(t, srv, bidder, uuid.NewString(), 12_500)
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "lot-not-found", body["slug"])
}

func TestHistory_NewestFirst(t *testing.T) {
	srv, _ := newTestApp(t)
	owner := newUser(t, srv)
	first := newUser(t, srv)
	second := newUser(t, srv)
	lotID := createLot(t, srv, owner)

	status, _ := placeBid(t, srv, first, lotID, 11_000)
	require.Equal(t, http.StatusCreated, status)
	status, _ = placeBid(t, srv, second, lotID, 12_000)
	require.Equal(t, http.StatusCreated, status)

	resp, body := testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/lots/"+lotID+"/bids", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, 2, body["total"])
	items, ok := body["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)
	assert.EqualValues(t, 12_000, items[0].(map[string]any)["amountMinor"], "newest bid first")
	assert.EqualValues(t, 11_000, items[1].(map[string]any)["amountMinor"])

	// Pagination: page 2 of size 1 holds the older bid.
	resp, body = testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/lots/"+lotID+"/bids?page=2&pageSize=1", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	items = body["items"].([]any)
	require.Len(t, items, 1)
	assert.EqualValues(t, 11_000, items[0].(map[string]any)["amountMinor"])
}

// TestPlaceBid_Race fires ten simultaneous bids with the same amount at
// one lot. The FOR UPDATE lock inside the placement transaction must
// let exactly one through; the rest find the price already at their
// amount and fail with bid-too-low.
func TestPlaceBid_Race(t *testing.T) {
	srv, _ := newTestApp(t)
	owner := newUser(t, srv)
	lotID := createLot(t, srv, owner)

	const bidders = 10
	const amount = 15_000
	tokens := make([]string, bidders)
	for i := range tokens {
		tokens[i] = newUser(t, srv)
	}

	start := make(chan struct{})
	statuses := make(chan int, bidders)
	errors := make(chan error, bidders)
	var wg sync.WaitGroup
	for i := 0; i < bidders; i++ {
		wg.Add(1)
		// Raw HTTP in the goroutines: testify's require must not be
		// called off the test goroutine.
		go func(token string) {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/lots/"+lotID+"/bids",
				strings.NewReader(`{"amountMinor":15000}`))
			if err != nil {
				errors <- err
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)

			<-start
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errors <- err
				return
			}
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}(tokens[i])
	}
	close(start)
	wg.Wait()
	close(statuses)
	close(errors)

	for err := range errors {
		require.NoError(t, err)
	}

	var created, conflicts int
	for code := range statuses {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	assert.Equal(t, 1, created, "exactly one of the simultaneous equal bids must win")
	assert.Equal(t, bidders-1, conflicts)

	lot := getLot(t, srv, lotID)
	assert.EqualValues(t, amount, lot["currentPriceMinor"])
	assert.EqualValues(t, 1, lot["bidCount"], "the losing bids must leave no trace on the lot")

	resp, history := testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/lots/"+lotID+"/bids", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.EqualValues(t, 1, history["total"], "only the winning bid may be recorded")
}
