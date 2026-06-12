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

	"molotlite/internal/platform/config"
	"molotlite/internal/platform/logs"
	"molotlite/internal/platform/postgres"
	"molotlite/internal/platform/server"
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

	// Feature migrations: append each feature's set here, e.g.
	//
	//	sets = append(sets, postgres.Migrations{
	//		FS:           users.MigrationsFS(),
	//		VersionTable: "goose_version_users",
	//	})
	var sets []postgres.Migrations
	if err := postgres.RunMigrations(ctx, cfg.DatabaseURL, sets); err != nil {
		return err
	}

	r := server.New(logger)
	server.RegisterHealth(r, logger, []server.ReadinessProbe{
		{Name: "postgres", Check: pool.Ping},
	})

	// Feature routes: construct the feature (storage → service → handler)
	// and register it here. Protected routes go behind the auth
	// middleware, public ones (login, signup) on the root router, e.g.
	//
	//	usersHandler := users.New(pool, cfg.JWTSecret, cfg.BcryptCost)
	//	usersHandler.RegisterPublic(r) // POST /api/auth/login, ...
	//
	//	r.Route("/api", func(api chi.Router) {
	//		api.Use(auth.Middleware(cfg.JWTSecret))
	//		lots.Register(api, lots.NewService(pool))
	//	})

	addr := fmt.Sprintf(":%d", cfg.HTTPPort)
	logger.Info("server starting", slog.String("addr", addr))
	return server.Run(ctx, addr, r)
}
