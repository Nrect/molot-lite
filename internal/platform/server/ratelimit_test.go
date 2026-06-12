package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func limitedHandler(rl *RateLimiter) http.Handler {
	return rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func doFrom(h http.Handler, remoteAddr string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/users/login", nil)
	r.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRateLimiter_BurstThen429(t *testing.T) {
	// rps 1 keeps the bucket from refilling mid-test; burst 3 is the
	// whole initial budget.
	h := limitedHandler(NewRateLimiter(1, 3))

	for i := 0; i < 3; i++ {
		assert.Equal(t, http.StatusOK, doFrom(h, "203.0.113.7:1000").Code, "request %d within burst", i+1)
	}

	w := doFrom(h, "203.0.113.7:1000")
	assert.Equal(t, http.StatusTooManyRequests, w.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "rate-limited", body["slug"])
}

func TestRateLimiter_IsolatesIPs(t *testing.T) {
	h := limitedHandler(NewRateLimiter(1, 1))

	assert.Equal(t, http.StatusOK, doFrom(h, "203.0.113.7:1000").Code)
	assert.Equal(t, http.StatusTooManyRequests, doFrom(h, "203.0.113.7:2000").Code,
		"same IP shares one bucket regardless of port")
	assert.Equal(t, http.StatusOK, doFrom(h, "203.0.113.8:1000").Code,
		"another IP gets its own bucket")
}

func TestRateLimiter_SweepEvictsIdleVisitors(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	h := limitedHandler(rl)

	require.Equal(t, http.StatusOK, doFrom(h, "203.0.113.7:1000").Code)
	require.Equal(t, http.StatusTooManyRequests, doFrom(h, "203.0.113.7:1000").Code)

	// Idle past the TTL: the janitor would drop the bucket; the test
	// calls the sweep directly instead of waiting for the ticker.
	rl.sweep(time.Now().Add(visitorTTL + time.Second))

	rl.mu.Lock()
	remaining := len(rl.visitors)
	rl.mu.Unlock()
	assert.Zero(t, remaining)

	assert.Equal(t, http.StatusOK, doFrom(h, "203.0.113.7:1000").Code,
		"a swept client starts over with a fresh burst")
}
