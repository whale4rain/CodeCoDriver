package server

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	clients map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, clients: map[string][]time.Time{}}
}

func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.limit <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		now := time.Now()
		key := clientIP(r)
		l.mu.Lock()
		requests := l.clients[key]
		cutoff := now.Add(-l.window)
		first := 0
		for first < len(requests) && requests[first].Before(cutoff) {
			first++
		}
		requests = requests[first:]
		if len(requests) >= l.limit {
			l.clients[key] = requests
			l.mu.Unlock()
			w.Header().Set("Retry-After", strconv.Itoa(int(l.window.Seconds())))
			problem(w, http.StatusTooManyRequests, errRateLimitExceeded)
			return
		}
		l.clients[key] = append(requests, now)
		l.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		return forwarded
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
