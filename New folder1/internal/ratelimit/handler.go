package ratelimit

import (
	"encoding/json"
	"net/http"
)

// Handler exposes the rate-limiter over HTTP.
type Handler struct {
	limiter *SlidingWindowLimiter
}

// NewHandler wraps a SlidingWindowLimiter in an HTTP handler.
func NewHandler(l *SlidingWindowLimiter) *Handler {
	return &Handler{limiter: l}
}

// ── shared helpers ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// ── POST /request ─────────────────────────────────────────────────────────────

type requestIn struct {
	UserID  string          `json:"user_id"`
	Payload json.RawMessage `json:"payload"`
}

type requestAccepted struct {
	Status          string `json:"status"`
	UserID          string `json:"user_id"`
	AcceptedInWindow int   `json:"accepted_in_window"`
	WindowSeconds   int    `json:"window_seconds"`
}

// HandleRequest handles POST /request.
// Returns 201 Created on success, 400 on bad input, 429 when rate limited.
func (h *Handler) HandleRequest(w http.ResponseWriter, r *http.Request) {
	var body requestIn

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody{
			Error:   "invalid_body",
			Message: "request body must be valid JSON with user_id (string) and payload fields",
		})
		return
	}

	if body.UserID == "" {
		writeJSON(w, http.StatusBadRequest, errBody{
			Error:   "missing_user_id",
			Message: "user_id is required and must not be empty",
		})
		return
	}

	// json.RawMessage is nil if the key was absent; "null" if explicitly null
	if len(body.Payload) == 0 {
		writeJSON(w, http.StatusBadRequest, errBody{
			Error:   "missing_payload",
			Message: "payload is required",
		})
		return
	}

	result := h.limiter.Allow(body.UserID)

	if !result.Allowed {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, errBody{
			Error:   "rate_limit_exceeded",
			Message: "maximum 5 requests per 60-second rolling window reached; try again later",
		})
		return
	}

	// 201 Created — the request has been accepted for processing
	writeJSON(w, http.StatusCreated, requestAccepted{
		Status:           "accepted",
		UserID:           body.UserID,
		AcceptedInWindow: result.AcceptedInWindow,
		WindowSeconds:    60,
	})
}

// ── GET /stats ────────────────────────────────────────────────────────────────

type statsResponse struct {
	Users []UserStats `json:"users"`
	Count int         `json:"total_tracked_users"`
	Note  string      `json:"note"`
}

// HandleStats handles GET /stats.
// Returns per-user accepted-in-window and cumulative rejected counts.
func (h *Handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	users := h.limiter.Stats()
	writeJSON(w, http.StatusOK, statsResponse{
		Users: users,
		Count: len(users),
		Note:  "rejected_cumulative is a lifetime counter; accepted_in_window reflects the current 60-second rolling window",
	})
}
