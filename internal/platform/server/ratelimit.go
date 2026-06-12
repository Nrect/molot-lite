package server

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"

	"molotlite/internal/platform/errs"
)

// visitorTTL is how long an idle client keeps its bucket; the janitor
// removes anything quiet for longer, so the map cannot grow unbounded.
const visitorTTL = 3 * time.Minute

// RateLimiter is a per-IP token bucket for abuse-prone endpoints
// (registration, login). PER-INSTANCE ONLY: state lives in process
// memory, so a second replica multiplies the effective limit and a
// restart resets it. That is the right trade for an MVP on one box;
// the moment a second replica appears, move the counters to Redis
// (documented as a maturation trigger in docs/MVP-RULES.md).
type RateLimiter struct {
	rps   int
	burst int

	mu       sync.Mutex
	visitors map[string]*visitor
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter builds a limiter allowing rps sustained requests per
// second with the given burst per client IP, and starts the janitor
// goroutine that evicts idle clients. Create one per process.
func NewRateLimiter(rps, burst int) *RateLimiter {
	rl := &RateLimiter{rps: rps, burst: burst, visitors: make(map[string]*visitor)}
	go rl.janitor()
	return rl
}

// Middleware rejects clients over their budget with
// 429 {"slug":"rate-limited"} through the shared errs mapper.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(clientIP(r)) {
			errs.Respond(w, r, errs.TooManyRequests("rate-limited"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP prefers the address recorded by the client-IP middleware in
// server.New (peer address, or trusted XFF behind a proxy) and falls
// back to the bare connection address when the middleware is absent.
func clientIP(r *http.Request) string {
	if ip := middleware.GetClientIP(r.Context()); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (rl *RateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, ok := rl.visitors[ip]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(rate.Limit(rl.rps), rl.burst)}
		rl.visitors[ip] = v
	}
	v.lastSeen = time.Now()
	return v.limiter.Allow()
}

// janitor sweeps idle visitors once a minute for the life of the
// process (the limiter is wired once in main and never torn down).
func (rl *RateLimiter) janitor() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.sweep(time.Now())
	}
}

// sweep drops visitors idle longer than visitorTTL as of now; split
// from the janitor loop so tests can trigger eviction directly.
func (rl *RateLimiter) sweep(now time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	for ip, v := range rl.visitors {
		if now.Sub(v.lastSeen) > visitorTTL {
			delete(rl.visitors, ip)
		}
	}
}
