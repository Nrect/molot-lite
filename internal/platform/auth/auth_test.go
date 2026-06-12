package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"molotlite/internal/platform/errs"
)

const testSecret = "0123456789abcdef0123456789abcdef" // 32 bytes

// echoHandler responds 200 with the user id the middleware put into the
// context — the full production round-trip.
func echoHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := UserID(r.Context())
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(id.String()))
	})
}

func TestMiddleware_ValidToken(t *testing.T) {
	userID := uuid.New()
	token, err := GenerateToken(testSecret, userID, time.Hour)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	Middleware(testSecret)(echoHandler(t)).ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, userID.String(), w.Body.String())
}

func TestMiddleware_Rejects(t *testing.T) {
	expired, err := GenerateToken(testSecret, uuid.New(), -time.Hour)
	require.NoError(t, err)
	foreign, err := GenerateToken(strings.Repeat("x", 32), uuid.New(), time.Hour)
	require.NoError(t, err)

	tests := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"not a bearer token", "Basic abc"},
		{"garbage token", "Bearer not-a-jwt"},
		{"expired token", "Bearer " + expired},
		{"wrong secret", "Bearer " + foreign},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				r.Header.Set("Authorization", tt.header)
			}

			called := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
			Middleware(testSecret)(next).ServeHTTP(w, r)

			assert.False(t, called, "handler must not run on auth failure")
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.JSONEq(t, `{"slug":"unauthorized"}`, w.Body.String())
		})
	}
}

func TestUserID_MissingFromContext(t *testing.T) {
	_, err := UserID(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, errs.Internal("no-user-in-context"))
}

func TestContextWithUserID_RoundTrip(t *testing.T) {
	want := uuid.New()
	got, err := UserID(ContextWithUserID(context.Background(), want))

	require.NoError(t, err)
	assert.Equal(t, want, got)
}
