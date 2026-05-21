// Package catalog implements the product-catalogue store and HTTP handlers.
//
// Memory layout
// ─────────────
//  products  map[id] → *Product        full record (all URLs)
//  meta      map[id] → ProductMeta     lightweight record (counts only)
//  skuIndex  map[sku] → id             uniqueness + O(1) look-up
//  idOrder   []string                  insertion-ordered IDs for stable pagination
//
// GET /products reads only from meta — it never touches the full URL slices.
// GET /products/{id} reads from products — it returns all URLs.
// This separation means listing 1,000 products with 10 images each loads
// exactly 20 meta records, not 10,000 URL strings.
package catalog

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ── constants ─────────────────────────────────────────────────────────────────

const (
	MaxURLsPerRequest = 20   // max image_urls or video_urls per single request
	MaxURLLength      = 2048 // RFC 7230 recommended practical limit
	DefaultLimit      = 20
	MaxLimit          = 100
)

// ── sentinel errors ───────────────────────────────────────────────────────────

var (
	ErrNotFound     = errors.New("product not found")
	ErrDuplicateSKU = errors.New("SKU already exists")
	ErrValidation   = errors.New("validation error")
)

// ── domain types ──────────────────────────────────────────────────────────────

// Product is the full record stored in memory and returned by GET /products/{id}.
type Product struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	SKU       string    `json:"sku"`
	ImageURLs []string  `json:"image_urls"`
	VideoURLs []string  `json:"video_urls"`
	CreatedAt time.Time `json:"created_at"`
}

// ProductMeta is the lightweight index entry used exclusively by the list endpoint.
// It intentionally omits URL arrays so listing never deserialises them.
type ProductMeta struct {
	ID           string
	Name         string
	SKU          string
	ImageCount   int
	VideoCount   int
	ThumbnailURL string // first image URL, or "" if none
	CreatedAt    time.Time
}

// ListItem is the JSON shape returned by GET /products.
type ListItem struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	SKU          string    `json:"sku"`
	ImageCount   int       `json:"image_count"`
	VideoCount   int       `json:"video_count"`
	ThumbnailURL string    `json:"thumbnail_url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// ── inputs ────────────────────────────────────────────────────────────────────

// CreateInput is the decoded request body for POST /products.
type CreateInput struct {
	Name      string   `json:"name"`
	SKU       string   `json:"sku"`
	ImageURLs []string `json:"image_urls"`
	VideoURLs []string `json:"video_urls"`
}

// AddMediaInput is the decoded request body for POST /products/{id}/media.
type AddMediaInput struct {
	ImageURLs []string `json:"image_urls"`
	VideoURLs []string `json:"video_urls"`
}

// ── store ─────────────────────────────────────────────────────────────────────

// Store is safe for concurrent use. A single sync.RWMutex guards all fields.
type Store struct {
	mu       sync.RWMutex
	products map[string]*Product
	meta     map[string]*ProductMeta
	skuIndex map[string]string // sku → id
	idOrder  []string          // append-only; gives stable pagination
	seq      int64             // monotone counter for ID generation
}

// NewStore returns a ready-to-use Store.
func NewStore() *Store {
	return &Store{
		products: make(map[string]*Product),
		meta:     make(map[string]*ProductMeta),
		skuIndex: make(map[string]string),
	}
}

// ── ID generation ─────────────────────────────────────────────────────────────

// nextID generates a short, human-readable unique ID.
// Called only while holding the write lock, so the counter is safe.
func (s *Store) nextID() string {
	s.seq++
	// e.g. "p_1748123456789_1"  – nano timestamp + sequence
	return fmt.Sprintf("p_%d_%d", time.Now().UnixNano(), s.seq)
}

// ── validation ────────────────────────────────────────────────────────────────

// validateURLList checks count limits and URL format for a slice of URLs.
func validateURLList(field string, urls []string) error {
	if len(urls) > MaxURLsPerRequest {
		return fmt.Errorf("%w: %s exceeds maximum of %d URLs per request",
			ErrValidation, field, MaxURLsPerRequest)
	}
	for i, raw := range urls {
		if len(raw) > MaxURLLength {
			return fmt.Errorf("%w: %s[%d] exceeds maximum URL length of %d",
				ErrValidation, field, i, MaxURLLength)
		}
		if strings.TrimSpace(raw) == "" {
			return fmt.Errorf("%w: %s[%d] must not be blank", ErrValidation, field, i)
		}
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("%w: %s[%d] %q is not a valid http/https URL",
				ErrValidation, field, i, raw)
		}
	}
	return nil
}

// ── public methods ────────────────────────────────────────────────────────────

// Create validates input, checks SKU uniqueness, and inserts a new product.
func (s *Store) Create(in CreateInput) (*Product, error) {
	// Validate before acquiring the lock (cheap checks first)
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: name must not be empty", ErrValidation)
	}
	if strings.TrimSpace(in.SKU) == "" {
		return nil, fmt.Errorf("%w: sku must not be empty", ErrValidation)
	}
	if err := validateURLList("image_urls", in.ImageURLs); err != nil {
		return nil, err
	}
	if err := validateURLList("video_urls", in.VideoURLs); err != nil {
		return nil, err
	}

	// Normalise nil slices so JSON always emits [] not null
	imgs := normalise(in.ImageURLs)
	vids := normalise(in.VideoURLs)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.skuIndex[in.SKU]; dup {
		return nil, ErrDuplicateSKU
	}

	id := s.nextID()
	now := time.Now()

	p := &Product{
		ID:        id,
		Name:      in.Name,
		SKU:       in.SKU,
		ImageURLs: imgs,
		VideoURLs: vids,
		CreatedAt: now,
	}

	thumbnail := ""
	if len(imgs) > 0 {
		thumbnail = imgs[0]
	}

	s.products[id] = p
	s.meta[id] = &ProductMeta{
		ID:           id,
		Name:         in.Name,
		SKU:          in.SKU,
		ImageCount:   len(imgs),
		VideoCount:   len(vids),
		ThumbnailURL: thumbnail,
		CreatedAt:    now,
	}
	s.skuIndex[in.SKU] = id
	s.idOrder = append(s.idOrder, id)

	return copyProduct(p), nil
}

// List returns a paginated slice of lightweight ListItem values.
// It reads only from s.meta — URL slices are never touched.
func (s *Store) List(offset, limit int) (items []ListItem, total int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total = len(s.idOrder)
	if offset >= total || limit == 0 {
		return []ListItem{}, total
	}

	end := offset + limit
	if end > total {
		end = total
	}

	page := s.idOrder[offset:end]
	items = make([]ListItem, 0, len(page))
	for _, id := range page {
		m := s.meta[id]
		items = append(items, ListItem{
			ID:           m.ID,
			Name:         m.Name,
			SKU:          m.SKU,
			ImageCount:   m.ImageCount,
			VideoCount:   m.VideoCount,
			ThumbnailURL: m.ThumbnailURL,
			CreatedAt:    m.CreatedAt,
		})
	}
	return items, total
}

// GetByID returns a full copy of the product, including all URL arrays.
func (s *Store) GetByID(id string) (*Product, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.products[id]
	if !ok {
		return nil, ErrNotFound
	}
	return copyProduct(p), nil
}

// AddMedia appends URLs to an existing product's media lists.
// Both the product record and the meta index are updated atomically.
func (s *Store) AddMedia(id string, in AddMediaInput) (*Product, error) {
	if len(in.ImageURLs) == 0 && len(in.VideoURLs) == 0 {
		return nil, fmt.Errorf("%w: at least one of image_urls or video_urls must be provided", ErrValidation)
	}
	if err := validateURLList("image_urls", in.ImageURLs); err != nil {
		return nil, err
	}
	if err := validateURLList("video_urls", in.VideoURLs); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.products[id]
	if !ok {
		return nil, ErrNotFound
	}

	p.ImageURLs = append(p.ImageURLs, in.ImageURLs...)
	p.VideoURLs = append(p.VideoURLs, in.VideoURLs...)

	// Keep meta in sync
	m := s.meta[id]
	m.ImageCount = len(p.ImageURLs)
	m.VideoCount = len(p.VideoURLs)
	if m.ThumbnailURL == "" && len(p.ImageURLs) > 0 {
		m.ThumbnailURL = p.ImageURLs[0]
	}

	return copyProduct(p), nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// normalise converts nil to an empty slice (avoids JSON null).
func normalise(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// copyProduct returns a deep copy so callers cannot mutate store internals.
func copyProduct(p *Product) *Product {
	c := *p
	c.ImageURLs = append([]string(nil), p.ImageURLs...)
	c.VideoURLs = append([]string(nil), p.VideoURLs...)
	return &c
}
