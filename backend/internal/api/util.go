package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// statusClientClosed (499, nginx's "client closed request") marks a request the
// client aborted before we finished. It is deliberately a 4xx: an abort is not a
// server fault, so it stays out of the 5xx error-rate alerting.
const statusClientClosed = 499

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	// PublicPath accompanies code=forbidden_public only — the no-login reader
	// route for content denied on the authed route but published public.
	PublicPath string `json:"public_path,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: message, Code: code})
}

// clientCanceled reports whether err is the request's own context being
// canceled — i.e. the client went away mid-request (TanStack Query cancels a
// superseded search-as-you-type fetch, the user navigated away, etc.). That is
// not a server error, so the caller should stop and NOT write a 5xx: this writes
// 499 and returns true. It's tied to r.Context().Err() so an internal
// context.Canceled unrelated to the request still surfaces as a real 500.
func clientCanceled(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		w.WriteHeader(statusClientClosed)
		return true
	}
	return false
}

// parseIDParam extracts a positive int64 path param from a Go 1.22+ mux route.
// On failure it writes a 400 envelope and returns ok=false.
func parseIDParam(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "path parameter '"+name+"' must be a positive integer")
		return 0, false
	}
	return id, true
}
