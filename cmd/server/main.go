// Command server is the single process of the service: it loads the
// configuration, wires the platform, applies feature migrations and
// serves HTTP until SIGINT/SIGTERM.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-chi/chi/v5"

	"molotlite/internal/bids"
	"molotlite/internal/lots"
	"molotlite/internal/notify"
	"molotlite/internal/platform/auth"
	"molotlite/internal/platform/config"
	"molotlite/internal/platform/logs"
	"molotlite/internal/platform/postgres"
	"molotlite/internal/platform/server"
	"molotlite/internal/users"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logs.NewLogger(string(cfg.LogFormat))
	slog.SetDefault(logger)

	pool, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	sets := []postgres.Migrations{
		users.Migrations(),
		lots.Migrations(),
		bids.Migrations(),
	}
	if err := postgres.RunMigrations(ctx, cfg.DatabaseURL, sets); err != nil {
		return err
	}

	r := server.New(logger)
	// CORS sits on the root router so preflights are answered before
	// routing; with no configured origins it stays off entirely.
	if len(cfg.CORSOrigins) > 0 {
		r.Use(server.CORS(cfg.CORSOrigins))
	}
	health := server.RegisterHealth(r, logger, []server.ReadinessProbe{
		{Name: "postgres", Check: pool.Ping},
	})

	// Features: storage → service → handler, wired with plain calls.
	// bids depends on lots (PlaceBidTx) and lots on users (UserExists);
	// both are rule-2 service-function calls, never foreign tables.
	usersService := users.NewService(pool, cfg.JWTSecret, cfg.BcryptCost)
	lotsService := lots.NewService(pool, usersService)
	notifyService := notify.NewService(cfg.TelegramBotToken, cfg.TelegramChatID)
	bidsService := bids.NewService(pool, lotsService, notifyService)

	usersHandler := users.NewHandler(usersService)
	lotsHandler := lots.NewHandler(lotsService)
	bidsHandler := bids.NewHandler(bidsService)

	r.Route("/api", func(api chi.Router) {
		// Public browsing: lots and bid history, no token, no limiter.
		api.Group(func(public chi.Router) {
			lotsHandler.RegisterPublic(public)
			bidsHandler.RegisterPublic(public)
		})
		// Auth endpoints (signup, login) are the bot magnets, so they
		// sit behind the per-IP rate limiter. RATE_LIMIT_RPS=0 turns it
		// off.
		api.Group(func(public chi.Router) {
			if cfg.RateLimitRPS > 0 {
				public.Use(server.NewRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst).Middleware)
			}
			usersHandler.RegisterPublic(public)
		})
		// Protected: everything acting on behalf of a user.
		api.Group(func(protected chi.Router) {
			protected.Use(auth.Middleware(cfg.JWTSecret))
			usersHandler.RegisterProtected(protected)
			lotsHandler.RegisterProtected(protected)
			bidsHandler.RegisterProtected(protected)
		})
	})

	addr := fmt.Sprintf(":%d", cfg.HTTPPort)
	logger.Info("server starting", slog.String("addr", addr))
	// health.NotReady runs first on shutdown: readyz turns 503, the
	// load balancer drains the instance, then the listener closes.
	return server.Run(ctx, addr, r, health.NotReady)
}
