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
// the request log below and anything handlers log.
func requestIDContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := middleware.GetReqID(r.Context()); id != "" {
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

// RegisterHealth mounts GET /healthz (liveness, always 200) and
// GET /readyz (503 on the first failing probe) on r — the root router,
// outside any authenticated group.
func RegisterHealth(r chi.Router, logger *slog.Logger, probes []ReadinessProbe) {
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
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
}

// Run serves handler on addr until ctx is cancelled, then shuts down
// gracefully with a 10s drain budget. It returns when the server has
// fully stopped.
func Run(ctx context.Context, addr string, handler http.Handler) error {
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
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http server shutdown: %w", err)
		}
		return <-serveErr
	}
}
