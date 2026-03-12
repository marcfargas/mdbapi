package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type contextKey int

const reqIDKey contextKey = iota

// authMiddleware validates API keys using constant-time comparison.
// It supports multiple keys for zero-downtime rotation.
// Failed attempts are logged with source IP; a configurable delay discourages brute-force.
func authMiddleware(keys []string, failDelay time.Duration, next http.Handler) http.Handler {
	// Pre-hash keys so we can do constant-time comparison without leaking
	// key length information. SHA-256 normalises all keys to 32 bytes.
	type hashedKey [sha256.Size]byte
	hashes := make([]hashedKey, len(keys))
	for i, k := range keys {
		hashes[i] = sha256.Sum256([]byte(k))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supplied := sha256.Sum256([]byte(extractKey(r)))
		valid := false
		for _, h := range hashes {
			if subtle.ConstantTimeCompare(supplied[:], h[:]) == 1 {
				valid = true
				break
			}
		}
		if !valid {
			slog.Warn("auth failure",
				"remote_addr", r.RemoteAddr,
				"path", r.URL.Path,
				"method", r.Method,
			)
			if failDelay > 0 {
				time.Sleep(failDelay)
			}
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractKey reads the key from "Authorization: Bearer <key>" or "X-API-Key: <key>".
func extractKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if strings.HasPrefix(auth, "Bearer ") {
			return strings.TrimPrefix(auth, "Bearer ")
		}
	}
	return r.Header.Get("X-API-Key")
}

// ipAllowMiddleware rejects requests from IPs not in the allow list.
// If allowList is empty, all IPs are allowed.
func ipAllowMiddleware(allowList []string, next http.Handler) http.Handler {
	if len(allowList) == 0 {
		return next
	}

	// Parse CIDRs and individual IPs at startup.
	var nets []*net.IPNet
	var ips []net.IP
	for _, entry := range allowList {
		if strings.Contains(entry, "/") {
			_, cidr, err := net.ParseCIDR(entry)
			if err == nil {
				nets = append(nets, cidr)
			}
		} else {
			if ip := net.ParseIP(entry); ip != nil {
				ips = append(ips, ip)
			}
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := extractIP(r)
		ip := net.ParseIP(clientIP)
		if ip == nil {
			slog.Warn("ip_allow: unparseable client IP",
				"remote_addr", r.RemoteAddr,
			)
			writeError(w, http.StatusForbidden, "forbidden", "access denied")
			return
		}

		for _, allowed := range ips {
			if allowed.Equal(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}
		for _, cidr := range nets {
			if cidr.Contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}

		slog.Warn("ip_allow: rejected",
			"remote_addr", r.RemoteAddr,
			"client_ip", clientIP,
		)
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
	})
}

// extractIP returns the client IP from RemoteAddr, stripping the port.
func extractIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // might already be just an IP
	}
	return host
}

// rateLimitMiddleware enforces per-IP request rate limiting using a token bucket.
// rps is the sustained rate (requests/second), burst is the maximum burst size.
// If rps <= 0, rate limiting is disabled.
func rateLimitMiddleware(rps float64, burst int, next http.Handler) http.Handler {
	if rps <= 0 {
		return next
	}

	var (
		mu       sync.Mutex
		limiters = make(map[string]*rateLimiterEntry)
	)

	// Background cleanup of stale entries every 5 minutes.
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			mu.Lock()
			now := time.Now()
			for ip, entry := range limiters {
				if now.Sub(entry.lastSeen) > 10*time.Minute {
					delete(limiters, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)

		mu.Lock()
		entry, ok := limiters[ip]
		if !ok {
			entry = &rateLimiterEntry{
				limiter: rate.NewLimiter(rate.Limit(rps), burst),
			}
			limiters[ip] = entry
		}
		entry.lastSeen = time.Now()
		mu.Unlock()

		if !entry.limiter.Allow() {
			slog.Warn("rate_limit: exceeded",
				"remote_addr", r.RemoteAddr,
				"client_ip", ip,
			)
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}

		next.ServeHTTP(w, r)
	})
}

type rateLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// securityHeadersMiddleware sets defensive HTTP headers.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		next.ServeHTTP(w, r)
	})
}

// requestTimeoutMiddleware wraps each request context with a deadline.
func requestTimeoutMiddleware(timeout time.Duration, next http.Handler) http.Handler {
	if timeout <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// loggingMiddleware emits a structured log line per request.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

		// Generate a short request ID.
		reqID := fmt.Sprintf("%d", start.UnixNano())
		ctx := context.WithValue(r.Context(), reqIDKey, reqID)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-Id", reqID)

		next.ServeHTTP(rw, r)

		slog.Info("request",
			"req_id", reqID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// recoveryMiddleware converts panics to 500 responses and logs the stack trace.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered",
					"panic", fmt.Sprintf("%v", rec),
					"stack", string(debug.Stack()),
				)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// maxBodyMiddleware rejects requests whose bodies exceed the configured limit.
func maxBodyMiddleware(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
