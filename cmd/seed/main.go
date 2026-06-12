// Command seed fills the database with demo data — three users, a few
// open lots and a couple of bids — going strictly through the same
// service functions the HTTP handlers call, so every row passes the
// real validation and the real transactions.
//
// It is idempotent in the cheapest way that works: the first Register
// hitting email-taken means a previous run already seeded this
// database, so the command reports that and exits 0.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"molotlite/internal/bids"
	"molotlite/internal/lots"
	"molotlite/internal/notify"
	"molotlite/internal/platform/config"
	"molotlite/internal/platform/errs"
	"molotlite/internal/platform/logs"
	"molotlite/internal/platform/postgres"
	"molotlite/internal/users"
)

// demoPassword is shared by all demo users; the final output repeats it
// so the credentials are copy-pasteable into api.http.
const demoPassword = "demo-pass-123"

type demoUser struct {
	email       string
	displayName string
	id          uuid.UUID
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.SetDefault(logs.NewLogger(string(cfg.LogFormat)))

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

	// Same wiring as the server, minus HTTP; notifications stay
	// disabled — seeding must not message a real chat.
	usersService := users.NewService(pool, cfg.JWTSecret, cfg.BcryptCost)
	lotsService := lots.NewService(pool, usersService)
	bidsService := bids.NewService(pool, lotsService, notify.NewService("", ""))

	demo := []demoUser{
		{email: "alice@example.com", displayName: "Alice"},
		{email: "bob@example.com", displayName: "Bob"},
		{email: "carol@example.com", displayName: "Carol"},
	}
	for i := range demo {
		id, err := usersService.Register(ctx, demo[i].email, demoPassword, demo[i].displayName)
		if errors.Is(err, errs.Conflict("email-taken")) {
			fmt.Println("seed already applied; nothing to do")
			return nil
		}
		if err != nil {
			return fmt.Errorf("register %s: %w", demo[i].email, err)
		}
		demo[i].id = id
	}
	alice, bob, carol := demo[0], demo[1], demo[2]

	endsAt := time.Now().Add(72 * time.Hour)
	lotSpecs := []struct {
		owner       demoUser
		title       string
		description string
		startMinor  int64
	}{
		{alice, "Antique blacksmith hammer", "Hand-forged head, oak handle, late XIX century.", 50_00},
		{alice, "Vinyl: Kind of Blue, 1st press", "Plays clean, sleeve worn at the edges.", 120_00},
		{bob, "Mechanical keyboard, 1989", "Beige, loud, glorious. Untested PS/2 cable included.", 30_00},
		{carol, "Box of mixed transistors", "Roughly 400 pieces, unsorted. Sold as is.", 10_00},
	}

	lotIDs := make([]uuid.UUID, len(lotSpecs))
	for i, spec := range lotSpecs {
		id, err := lotsService.Create(ctx, spec.owner.id, lots.CreateInput{
			Title:           spec.title,
			Description:     spec.description,
			StartPriceMinor: spec.startMinor,
			Currency:        "USD",
			EndsAt:          endsAt,
		})
		if err != nil {
			return fmt.Errorf("create lot %q: %w", spec.title, err)
		}
		lotIDs[i] = id
	}

	// A small bidding war on the hammer (owner Alice, so Bob and Carol
	// bid) and one opening bid on the keyboard (owner Bob, Alice bids).
	bidSpecs := []struct {
		lotID  uuid.UUID
		bidder demoUser
		amount int64
	}{
		{lotIDs[0], bob, 55_00},
		{lotIDs[0], carol, 60_00},
		{lotIDs[0], bob, 70_00},
		{lotIDs[2], alice, 35_00},
	}
	for _, spec := range bidSpecs {
		if _, err := bidsService.Place(ctx, spec.lotID, spec.bidder.id, spec.amount); err != nil {
			return fmt.Errorf("bid %d by %s: %w", spec.amount, spec.bidder.email, err)
		}
	}

	fmt.Println("seed complete:")
	fmt.Printf("  users: %d, lots: %d, bids: %d\n", len(demo), len(lotSpecs), len(bidSpecs))
	fmt.Println("  demo credentials (password is shared):")
	for _, u := range demo {
		fmt.Printf("    %-20s %s\n", u.email, demoPassword)
	}
	return nil
}
