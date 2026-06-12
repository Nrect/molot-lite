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
	server.RegisterHealth(r, logger, []server.ReadinessProbe{
		{Name: "postgres", Check: pool.Ping},
	})

	// Features: storage → service → handler, wired with plain calls.
	// bids depends on lots (PlaceBidTx) and lots on users (UserExists);
	// both are rule-2 service-function calls, never foreign tables.
	usersService := users.NewService(pool, cfg.JWTSecret, cfg.BcryptCost)
	lotsService := lots.NewService(pool, usersService)
	bidsService := bids.NewService(pool, lotsService)

	usersHandler := users.NewHandler(usersService)
	lotsHandler := lots.NewHandler(lotsService)
	bidsHandler := bids.NewHandler(bidsService)

	r.Route("/api", func(api chi.Router) {
		// Public: signup, login, browsing lots and bid history.
		api.Group(func(public chi.Router) {
			usersHandler.RegisterPublic(public)
			lotsHandler.RegisterPublic(public)
			bidsHandler.RegisterPublic(public)
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
	return server.Run(ctx, addr, r)
}
