// Package server owns the HTTP edge: the chi router with the
// middleware stack, JSON request/response helpers, health endpoints
// with injectable readiness probes, and graceful startup/shutdown.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"molotlite/internal/platform/logs"
)

// New builds the root router with the platform middleware stack
// (RequestID → client IP from peer address → request id into the log
// context → request log → Recoverer). Features mount their routes on
// groups they create in main, typically:
//
//	r.Route("/api", func(api chi.Router) {
//		api.Use(auth.Middleware(cfg.JWTSecret))
//		lots.Register(api, lotsService)
//	})
//
// Unauthenticated routes (health, login) go on the root router.
func New(logger *slog.Logger) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	// chi deprecated middleware.RealIP as spoofable (GHSA-3fxj-6jh8-hvhx);
	// ClientIPFromRemoteAddr is its safe successor — it records the peer
	// address without trusting client-controlled headers. Behind a trusted
	// proxy, switch to middleware.ClientIPFromXFFTrustedProxies(n).
	r.Use(middleware.ClientIPFromRemoteAddr)
	r.Use(requestIDContext)
	r.Use(requestLogger(logger))
	r.Use(middleware.Recoverer)

	return r
}

// requestIDContext copies the chi request id into the logs context, so
// every record made with the request context carries request_id — both
// the request log below and anything handlers log. It also echoes the
// id back as X-Request-Id, so a client error report can be matched to
// the exact log records of its request.
func requestIDContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := middleware.GetReqID(r.Context()); id != "" {
			w.Header().Set("X-Request-Id", id)
			r = r.WithContext(logs.ContextWithRequestID(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	})
}

// requestLogger emits one slog record per served request; request_id
// comes from the context handler in platform/logs.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			defer func() {
				logger.LogAttrs(r.Context(), slog.LevelInfo, "http request served",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", ww.Status()),
					slog.Int("bytes", ww.BytesWritten()),
					slog.Duration("duration", time.Since(start)),
				)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

// ReadinessProbe is a named readiness check; readyz fails on the first
// failing probe and reports its name.
type ReadinessProbe struct {
	Name  string
	Check func(ctx context.Context) error
}

// Health is the readiness state of the process, owned by readyz. It
// exists for ordered graceful shutdown: the shutdown path flips it
// before draining, so a load balancer polling /readyz stops routing new
// traffic to an instance that is about to close its listener.
type Health struct {
	shuttingDown atomic.Bool
}

// NotReady makes /readyz answer 503 from now on. It is deliberately
// irreversible: a process that started shutting down never comes back.
func (h *Health) NotReady() { h.shuttingDown.Store(true) }

// RegisterHealth mounts GET /healthz (liveness, always 200) and
// GET /readyz (503 on the first failing probe) on r — the root router,
// outside any authenticated group. The returned Health flips readyz to
// 503 ahead of the probes; wire its NotReady as the Run shutdown hook.
func RegisterHealth(r chi.Router, logger *slog.Logger, probes []ReadinessProbe) *Health {
	h := &Health{}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		if h.shuttingDown.Load() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		for _, probe := range probes {
			if err := probe.Check(req.Context()); err != nil {
				logger.WarnContext(req.Context(), "readiness probe failed",
					slog.String("probe", probe.Name), slog.Any("error", err))
				http.Error(w, probe.Name+" not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})

	return h
}

// Run serves handler on addr until ctx is cancelled, then shuts down
// gracefully with a 10s drain budget. onShutdown (nil allowed) runs
// once, right before draining starts — the place to flip readiness to
// 503 so new traffic stops while in-flight requests finish. Run returns
// when the server has fully stopped.
func Run(ctx context.Context, addr string, handler http.Handler, onShutdown func()) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("http server: %w", err)
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		if onShutdown != nil {
			onShutdown()
		}
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http server shutdown: %w", err)
		}
		return <-serveErr
	}
}
