package middleware

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"rate-limiter/internal/limiter"
)

func RateLimit(l limiter.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identifier := extractIdentifier(r)

			result, err := l.Allow(r.Context(), identifier)
			if err != nil {
				// Fail-Open Pattern: Log fault and let traffic pass during outages
				log.Printf("Rate limiter error for client %s: %v", identifier, err)
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, 10))

			if !result.Allowed {
				w.Header().Set("Retry-After", strconv.FormatInt(result.ResetSec, 10))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"Too Many Requests"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func extractIdentifier(r *http.Request) string {
	// 1. Prioritize API Key or Auth Header if present
	if apiKey := r.Header.Get("X-API-Key"); apiKey != "" {
		return "key:" + apiKey
	}

	// 2. Cloudflare Connecting IP Header (Edge deployments)
	if cfIP := r.Header.Get("CF-Connecting-IP"); cfIP != "" {
		return "ip:" + strings.TrimSpace(cfIP)
	}

	// 3. X-Forwarded-For Chain (Proxy deployments)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		return "ip:" + strings.TrimSpace(ips[0])
	}

	// 4. Standard RemoteAddr Fallback
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "ip:" + r.RemoteAddr
	}
	return "ip:" + ip
}
