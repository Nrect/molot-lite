package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func corsHandler(t *testing.T, origins ...string) http.Handler {
	t.Helper()
	return CORS(origins)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot) // marker: the request reached the app
	}))
}

func TestCORS_Preflight(t *testing.T) {
	h := corsHandler(t, "https://app.example.com")

	r := httptest.NewRequest(http.MethodOptions, "/api/lots", nil)
	r.Header.Set("Origin", "https://app.example.com")
	r.Header.Set("Access-Control-Request-Method", http.MethodPost)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusNoContent, w.Code, "preflight is answered by the middleware, not the app")
	assert.Equal(t, "https://app.example.com", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "GET, POST, PATCH, DELETE", w.Header().Get("Access-Control-Allow-Methods"))
	assert.Equal(t, "Authorization, Content-Type", w.Header().Get("Access-Control-Allow-Headers"))
	assert.Equal(t, "300", w.Header().Get("Access-Control-Max-Age"))
	assert.Contains(t, w.Header().Values("Vary"), "Origin")
}

func TestCORS_AllowedOriginOnPlainRequest(t *testing.T) {
	h := corsHandler(t, "https://app.example.com")

	r := httptest.NewRequest(http.MethodGet, "/api/lots", nil)
	r.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusTeapot, w.Code, "non-preflight requests pass through to the app")
	assert.Equal(t, "https://app.example.com", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_ForeignOriginNotReflected(t *testing.T) {
	h := corsHandler(t, "https://app.example.com")

	r := httptest.NewRequest(http.MethodGet, "/api/lots", nil)
	r.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	assert.Equal(t, http.StatusTeapot, w.Code)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_NoOriginPassesThrough(t *testing.T) {
	h := corsHandler(t, "https://app.example.com")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/lots", nil))

	assert.Equal(t, http.StatusTeapot, w.Code)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.NotContains(t, w.Header().Values("Vary"), "Origin")
}
