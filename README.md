# Molot-lite — Go starter for fast MVPs that refuse to rot

A deliberately small template for freelance projects, internal tools and MVPs: **feature folders on the outside, plain code on the inside, hard boundaries everywhere**. It is the younger brother of [Molot](https://github.com/Nrect/molot) — a reference DDD/CQRS system for serious long-lived software — and is designed as its subset: a project born from this template grows into the full architecture one feature at a time, without a rewrite.

## Why another starter

MVPs do not rot because the code is simple. They rot because the code has no place to live: handlers reach into other features' tables, business rules get duplicated across "services", a `utils/` folder becomes a landfill, and six months later deleting a feature is scarier than adding one. This template fixes exactly that — with ten rules instead of fifty-five, all written down in **[docs/MVP-RULES.md](docs/MVP-RULES.md)** (Russian).

## What's inside

```
cmd/server/            single binary: config, platform, features, HTTP
internal/platform/     infrastructure only, zero business:
                       env config (fail-fast), slog, slug errors with a single
                       HTTP mapper, JWT auth, pgx + per-feature goose migrations,
                       chi server with health endpoints and graceful shutdown
internal/users/        example feature: register (bcrypt), login (JWT), profile
internal/lots/         example feature: CRUD + pagination + search, owner rules
internal/bids/         example feature: real concurrency done right —
                       FOR UPDATE bidding, race-tested (N goroutines, one winner)
docs/MVP-RULES.md      the contract: 10 rules + growth triggers
```

Each feature is one folder: `handler.go`, `service.go`, `storage.go`, `migrations/`, tests. Plain transaction-script logic — no aggregates, no CQRS, no events. Deleting a feature means deleting its folder and two lines in `main.go`.

## Quick start

```bash
cp .env.example .env
docker compose up -d          # postgres
make run                      # migrations run on start
# register → login → create lot → bid:
curl -X POST localhost:8080/api/users -d '{"email":"a@b.c","password":"secret123","displayName":"Alice"}'
```

Tests: `make test` (unit, no docker) · `make test-integration` (httptest against real Postgres, including the bid-race test).

## When to graduate

The template does not forbid complexity — it postpones it until proven necessary. Growth triggers (a wall of ifs, a second consumer of your data, a multi-step process, money) and the per-feature upgrade recipe live in [MVP-RULES.md](docs/MVP-RULES.md) and in Molot's book ([chapter 19](https://github.com/Nrect/molot/blob/main/docs/book/19-workshop.md)): the boundaries you keep here are the bounded contexts you will need there.
