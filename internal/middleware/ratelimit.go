package middleware

import (
	"log"
	"net"
	"net/http"
	"strconv"

	"rate-limiter/internal/limiter"
)

func RateLimit(sw *limiter.SlidingWindow) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := extractIP(r)

			result, err := sw.Allow(r.Context(), ip)
			if err != nil {
				// Fail-Open pattern: Log error and allow request to prevent cascade failure
				log.Printf("Rate limiter error for IP %s: %v", ip, err)
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, 10))

			if !result.Allowed {
				w.Header().Set("Retry-After", "60")
				http.Error(w, `{"error":"Too Many Requests"}`, http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
