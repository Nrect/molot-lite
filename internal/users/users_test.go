// Integration tests over real HTTP and a real Postgres (rule 10). They
// skip themselves when TEST_DATABASE_URL is not set.
package users_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"molotlite/internal/platform/auth"
	"molotlite/internal/testutil"
	"molotlite/internal/users"
)

const testPassword = "correct-horse-battery"

// newTestApp assembles the users feature exactly as main does: public
// and protected groups under /api, real middleware, real database.
func newTestApp(t *testing.T) *httptest.Server {
	t.Helper()
	pool := testutil.Pool(t)
	testutil.Migrate(t, pool, users.Migrations())

	handler := users.NewHandler(users.NewService(pool, testutil.JWTSecret, bcrypt.MinCost))

	r := chi.NewRouter()
	r.Route("/api", func(api chi.Router) {
		api.Group(handler.RegisterPublic)
		api.Group(func(protected chi.Router) {
			protected.Use(auth.Middleware(testutil.JWTSecret))
			handler.RegisterProtected(protected)
		})
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func uniqueEmail() string {
	return uuid.NewString() + "@example.com"
}

func registerUser(t *testing.T, srv *httptest.Server, email string) string {
	t.Helper()
	resp, body := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/users", "", map[string]string{
		"email":       email,
		"password":    testPassword,
		"displayName": "Test User",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	id, ok := body["id"].(string)
	require.True(t, ok, "201 body must carry the new id")
	return id
}

func login(t *testing.T, srv *httptest.Server, email, password string) (int, map[string]any) {
	t.Helper()
	resp, body := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/users/login", "", map[string]string{
		"email":    email,
		"password": password,
	})
	return resp.StatusCode, body
}

func TestRegister(t *testing.T) {
	srv := newTestApp(t)

	id := registerUser(t, srv, uniqueEmail())
	_, err := uuid.Parse(id)
	assert.NoError(t, err, "returned id must be a uuid")
}

func TestRegister_Validation(t *testing.T) {
	srv := newTestApp(t)

	tests := []struct {
		name string
		req  map[string]string
		slug string
	}{
		{"bad email", map[string]string{"email": "not-an-email", "password": testPassword, "displayName": "U"}, "invalid-email"},
		{"short password", map[string]string{"email": uniqueEmail(), "password": "1234567", "displayName": "U"}, "password-too-short"},
		{"missing display name", map[string]string{"email": uniqueEmail(), "password": testPassword, "displayName": "  "}, "display-name-required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/users", "", tt.req)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.Equal(t, tt.slug, body["slug"])
		})
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	srv := newTestApp(t)
	email := uniqueEmail()
	registerUser(t, srv, email)

	resp, body := testutil.DoJSON(t, http.MethodPost, srv.URL+"/api/users", "", map[string]string{
		"email":       email,
		"password":    testPassword,
		"displayName": "Copycat",
	})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "email-taken", body["slug"])
}

func TestLogin_And_Me(t *testing.T) {
	srv := newTestApp(t)
	email := uniqueEmail()
	id := registerUser(t, srv, email)

	status, body := login(t, srv, email, testPassword)
	require.Equal(t, http.StatusOK, status)
	token, ok := body["token"].(string)
	require.True(t, ok, "login must return a token")

	resp, profile := testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/users/me", token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, id, profile["id"])
	assert.Equal(t, email, profile["email"])
	assert.Equal(t, "Test User", profile["displayName"])

	// The password hash must never appear in a response, under any key.
	for key := range profile {
		assert.NotContains(t, key, "assword", "profile leaks a password field: %s", key)
		assert.NotContains(t, key, "ash", "profile leaks a hash field: %s", key)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	srv := newTestApp(t)
	email := uniqueEmail()
	registerUser(t, srv, email)

	status, body := login(t, srv, email, "wrong-password-123")
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, "invalid-credentials", body["slug"])
}

func TestLogin_UnknownEmail_SameAnswer(t *testing.T) {
	srv := newTestApp(t)

	// Unknown email and wrong password must be indistinguishable.
	status, body := login(t, srv, uniqueEmail(), testPassword)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, "invalid-credentials", body["slug"])
}

func TestMe_RequiresToken(t *testing.T) {
	srv := newTestApp(t)

	resp, body := testutil.DoJSON(t, http.MethodGet, srv.URL+"/api/users/me", "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "unauthorized", body["slug"])
}
