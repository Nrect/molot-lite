package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"molotlite/internal/platform/errs"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestHealthEndpoints(t *testing.T) {
	r := New(discardLogger())
	RegisterHealth(r, discardLogger(), []ReadinessProbe{
		{Name: "ok-probe", Check: func(context.Context) error { return nil }},
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, w.Code)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestReadyz_FailingProbe(t *testing.T) {
	r := New(discardLogger())
	RegisterHealth(r, discardLogger(), []ReadinessProbe{
		{Name: "postgres", Check: func(context.Context) error { return errors.New("down") }},
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "postgres")
}

func TestReadyz_NotReadySkipsProbes(t *testing.T) {
	probeCalls := 0
	r := New(discardLogger())
	health := RegisterHealth(r, discardLogger(), []ReadinessProbe{
		{Name: "postgres", Check: func(context.Context) error { probeCalls++; return nil }},
	})

	health.NotReady()

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "shutting down")
	assert.Zero(t, probeCalls, "a shutting-down instance must not touch its probes")

	// healthz stays 200: the process is alive, just not accepting work.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestXRequestIDHeader(t *testing.T) {
	r := New(discardLogger())
	r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))

	assert.NotEmpty(t, w.Header().Get("X-Request-Id"))
}

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	t.Run("valid body", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"lot"}`))

		var p payload
		require.NoError(t, DecodeJSON(r, &p))
		assert.Equal(t, "lot", p.Name)
	})

	t.Run("malformed json", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":`))

		var p payload
		err := DecodeJSON(r, &p)
		assert.ErrorIs(t, err, errs.BadRequest("invalid-json"))
	})

	t.Run("trailing garbage", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"a"} trailing`))

		var p payload
		err := DecodeJSON(r, &p)
		assert.ErrorIs(t, err, errs.BadRequest("invalid-json"))
	})

	t.Run("body over limit", func(t *testing.T) {
		huge := `{"name":"` + strings.Repeat("x", maxBodyBytes+1) + `"}`
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(huge))

		var p payload
		err := DecodeJSON(r, &p)
		assert.ErrorIs(t, err, errs.BadRequest("body-too-large"))
	})
}

func TestRespondJSON(t *testing.T) {
	t.Run("with body", func(t *testing.T) {
		w := httptest.NewRecorder()
		RespondJSON(w, http.StatusCreated, map[string]string{"id": "42"})

		assert.Equal(t, http.StatusCreated, w.Code)
		assert.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
		assert.JSONEq(t, `{"id":"42"}`, w.Body.String())
	})

	t.Run("nil body", func(t *testing.T) {
		w := httptest.NewRecorder()
		RespondJSON(w, http.StatusNoContent, nil)

		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, w.Body.String())
	})
}

func TestRun_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "127.0.0.1:0", http.NewServeMux(), nil)
	}()

	time.Sleep(50 * time.Millisecond) // let ListenAndServe start
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestRun_CallsShutdownHookOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var hookCalls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, "127.0.0.1:0", http.NewServeMux(), func() {
			hookCalls.Add(1)
		})
	}()

	time.Sleep(50 * time.Millisecond) // let ListenAndServe start
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err)
		assert.EqualValues(t, 1, hookCalls.Load())
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
