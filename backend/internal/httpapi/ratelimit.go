package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter is a per-key token bucket bounding abuse of the free, unauthed
// creation endpoints (guest sessions, requests that bill the fallback LLM,
// registrations). It is in-process, so under multiple replicas it bounds abuse
// per pod rather than globally — a deliberate first cut; a Redis-backed global
// limiter can replace it later without touching call sites.
type rateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*tokenBucket
	ratePerSec float64
	burst      float64
	clock      func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMin, burst int) *rateLimiter {
	if perMin <= 0 {
		return nil // disabled
	}
	if burst <= 0 {
		burst = perMin
	}
	return &rateLimiter{
		buckets:    make(map[string]*tokenBucket),
		ratePerSec: float64(perMin) / 60.0,
		burst:      float64(burst),
		clock:      time.Now,
	}
}

func (rl *rateLimiter) allow(key string) bool {
	if rl == nil {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.clock()
	b := rl.buckets[key]
	if b == nil {
		if len(rl.buckets) > 20000 {
			rl.sweepLocked(now)
		}
		rl.buckets[key] = &tokenBucket{tokens: rl.burst - 1, last: now}
		return true
	}
	b.tokens += now.Sub(b.last).Seconds() * rl.ratePerSec
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

func (rl *rateLimiter) sweepLocked(now time.Time) {
	for k, b := range rl.buckets {
		if now.Sub(b.last) > 10*time.Minute {
			delete(rl.buckets, k)
		}
	}
}

// rateLimited wraps a handler, throttling POST requests by client IP.
func (s *Server) rateLimited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !s.limiter.allow(clientIP(r, s.trustedHops)) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "请求太频繁，稍等一下再试。")
			return
		}
		h(w, r)
	}
}

// clientIP derives the real client IP. X-Forwarded-For is attacker-controlled on
// the left (a client can prepend fakes); a trusted reverse proxy appends the
// real peer on the right. So we take the trustedHops-th entry FROM THE RIGHT,
// never the leftmost. With no proxy (trustedHops<=0) or no XFF, use RemoteAddr.
func clientIP(r *http.Request, trustedHops int) string {
	if trustedHops > 0 {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			idx := len(parts) - trustedHops
			if idx < 0 {
				idx = 0
			}
			if ip := strings.TrimSpace(parts[idx]); ip != "" {
				return ip
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
