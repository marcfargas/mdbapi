// Package api implements the HTTP REST layer.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// envelope is the standard JSON response wrapper.
type envelope struct {
	Data  interface{} `json:"data,omitempty"`
	Error *apiError   `json:"error,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: data})
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Error: &apiError{Code: code, Message: message}})
}

func notFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "resource not found")
}

// internalError logs the real error server-side and returns a generic message
// to the client. Never expose internal details (paths, driver strings, etc.).
func internalError(w http.ResponseWriter, err error) {
	slog.Error("internal error", "err", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
}

func badRequest(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, "bad_request", msg)
}

func forbidden(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusForbidden, "forbidden", msg)
}
