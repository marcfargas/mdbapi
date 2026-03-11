package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type contextKey int

const reqIDKey contextKey = iota

// authMiddleware validates API keys from Authorization or X-API-Key headers.
// It supports multiple keys for zero-downtime rotation.
// Failed attempts are logged with source IP; a configurable delay discourages brute-force.
func authMiddleware(keys []string, failDelay time.Duration, next http.Handler) http.Handler {
	validKeys := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		validKeys[k] = struct{}{}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := extractKey(r)
		if _, ok := validKeys[key]; !ok {
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
				internalError(w, fmt.Errorf("internal server error"))
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
