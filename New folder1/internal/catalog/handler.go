package catalog

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Handler wires the Store to HTTP using Go 1.22 pattern routing.
type Handler struct {
	store *Store
}

// NewHandler returns a Handler and registers all routes on mux.
func NewHandler(store *Store, mux *http.ServeMux) *Handler {
	h := &Handler{store: store}

	mux.HandleFunc("POST /products", h.createProduct)
	mux.HandleFunc("GET /products", h.listProducts)
	mux.HandleFunc("GET /products/{id}", h.getProduct)
	mux.HandleFunc("POST /products/{id}/media", h.addMedia)

	return h
}

// ── shared helpers ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type apiErr struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func badRequest(w http.ResponseWriter, code, msg string) {
	writeJSON(w, http.StatusBadRequest, apiErr{Error: code, Message: msg})
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, apiErr{Error: "not_found", Message: "product not found"})
}

func internalErr(w http.ResponseWriter) {
	writeJSON(w, http.StatusInternalServerError, apiErr{Error: "internal_error", Message: "unexpected server error"})
}

// ── POST /products ────────────────────────────────────────────────────────────

func (h *Handler) createProduct(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		badRequest(w, "invalid_json", "request body must be valid JSON: "+err.Error())
		return
	}

	p, err := h.store.Create(in)
	if err != nil {
		switch {
		case errors.Is(err, ErrDuplicateSKU):
			writeJSON(w, http.StatusConflict, apiErr{
				Error:   "duplicate_sku",
				Message: "a product with this SKU already exists",
			})
		case errors.Is(err, ErrValidation):
			badRequest(w, "validation_error", err.Error())
		default:
			internalErr(w)
		}
		return
	}

	writeJSON(w, http.StatusCreated, p)
}

// ── GET /products ─────────────────────────────────────────────────────────────

type listEnvelope struct {
	Data   []ListItem `json:"data"`
	Total  int        `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

func (h *Handler) listProducts(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parsePagination(w, r)
	if !ok {
		return
	}

	items, total := h.store.List(offset, limit)

	writeJSON(w, http.StatusOK, listEnvelope{
		Data:   items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// ── GET /products/{id} ────────────────────────────────────────────────────────

func (h *Handler) getProduct(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	p, err := h.store.GetByID(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			notFound(w)
			return
		}
		internalErr(w)
		return
	}

	writeJSON(w, http.StatusOK, p)
}

// ── POST /products/{id}/media ─────────────────────────────────────────────────

func (h *Handler) addMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var in AddMediaInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		badRequest(w, "invalid_json", "request body must be valid JSON: "+err.Error())
		return
	}

	p, err := h.store.AddMedia(id, in)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			notFound(w)
		case errors.Is(err, ErrValidation):
			badRequest(w, "validation_error", err.Error())
		default:
			internalErr(w)
		}
		return
	}

	writeJSON(w, http.StatusOK, p)
}

// ── pagination helper ─────────────────────────────────────────────────────────

// parsePagination reads ?limit=&offset= from the query string.
// It writes a 400 response and returns false on any parse error.
func parsePagination(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	q := r.URL.Query()
	limit = DefaultLimit
	offset = 0

	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			badRequest(w, "invalid_param", "limit must be a positive integer")
			return 0, 0, false
		}
		if v > MaxLimit {
			v = MaxLimit // silently cap rather than error
		}
		limit = v
	}

	if raw := q.Get("offset"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			badRequest(w, "invalid_param", "offset must be a non-negative integer")
			return 0, 0, false
		}
		offset = v
	}

	return limit, offset, true
}
