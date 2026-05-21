package catalog

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func goodCreate(name, sku string) CreateInput {
	return CreateInput{
		Name:      name,
		SKU:       sku,
		ImageURLs: []string{"https://cdn.example.com/img1.jpg"},
		VideoURLs: []string{"https://cdn.example.com/demo.mp4"},
	}
}

// TestCreateAndGet round-trips a product through Create then GetByID.
func TestCreateAndGet(t *testing.T) {
	s := NewStore()
	p, err := s.Create(goodCreate("Widget A", "SKU-001"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID == "" {
		t.Fatal("ID must not be empty")
	}

	got, err := s.GetByID(p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SKU != "SKU-001" || got.Name != "Widget A" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if len(got.ImageURLs) != 1 || len(got.VideoURLs) != 1 {
		t.Fatal("URL slices not preserved")
	}
}

// TestDuplicateSKU verifies 409-style rejection.
func TestDuplicateSKU(t *testing.T) {
	s := NewStore()
	s.Create(goodCreate("Widget A", "SKU-DUP"))
	_, err := s.Create(goodCreate("Widget A v2", "SKU-DUP"))
	if !errors.Is(err, ErrDuplicateSKU) {
		t.Fatalf("want ErrDuplicateSKU, got %v", err)
	}
}

// TestValidationEmptyName checks that blank name is rejected.
func TestValidationEmptyName(t *testing.T) {
	s := NewStore()
	_, err := s.Create(CreateInput{SKU: "SKU-X"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("want ErrValidation for empty name, got %v", err)
	}
}

// TestValidationBadURL rejects non-http/https URLs.
func TestValidationBadURL(t *testing.T) {
	s := NewStore()
	_, err := s.Create(CreateInput{
		Name:      "Widget",
		SKU:       "SKU-Y",
		ImageURLs: []string{"ftp://bad-scheme.com/img.jpg"},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("want ErrValidation for ftp:// URL, got %v", err)
	}
}

// TestValidationTooManyURLs enforces the per-request cap.
func TestValidationTooManyURLs(t *testing.T) {
	s := NewStore()
	urls := make([]string, MaxURLsPerRequest+1)
	for i := range urls {
		urls[i] = "https://cdn.example.com/img.jpg"
	}
	_, err := s.Create(CreateInput{Name: "W", SKU: "SKU-Z", ImageURLs: urls})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("want ErrValidation for >%d URLs, got %v", MaxURLsPerRequest, err)
	}
}

// TestListDoesNotReturnURLs ensures the list projection omits URL slices.
func TestListDoesNotReturnURLs(t *testing.T) {
	s := NewStore()
	s.Create(goodCreate("A", "SKU-1"))
	s.Create(goodCreate("B", "SKU-2"))

	items, total := s.List(0, 10)
	if total != 2 {
		t.Fatalf("total: want 2, got %d", total)
	}
	for _, item := range items {
		// ListItem has no URL fields — compile-time guarantee.
		// Just check counts are correct.
		if item.ImageCount != 1 || item.VideoCount != 1 {
			t.Fatalf("counts wrong: %+v", item)
		}
	}
}

// TestListPagination verifies offset/limit slicing.
func TestListPagination(t *testing.T) {
	s := NewStore()
	for i := 0; i < 10; i++ {
		s.Create(CreateInput{Name: "P", SKU: strings.Repeat("x", i+1)})
	}
	items, total := s.List(3, 4)
	if total != 10 {
		t.Fatalf("total: want 10, got %d", total)
	}
	if len(items) != 4 {
		t.Fatalf("page size: want 4, got %d", len(items))
	}
}

// TestAddMedia appends URLs and updates counts atomically.
func TestAddMedia(t *testing.T) {
	s := NewStore()
	p, _ := s.Create(goodCreate("Widget", "SKU-M"))

	updated, err := s.AddMedia(p.ID, AddMediaInput{
		ImageURLs: []string{"https://cdn.example.com/img2.jpg"},
	})
	if err != nil {
		t.Fatalf("AddMedia: %v", err)
	}
	if len(updated.ImageURLs) != 2 {
		t.Fatalf("want 2 images after append, got %d", len(updated.ImageURLs))
	}

	// Meta must reflect the new count
	items, _ := s.List(0, 10)
	if items[0].ImageCount != 2 {
		t.Fatalf("meta image_count: want 2, got %d", items[0].ImageCount)
	}
}

// TestAddMediaEmptyBody ensures empty input is rejected.
func TestAddMediaEmptyBody(t *testing.T) {
	s := NewStore()
	p, _ := s.Create(goodCreate("Widget", "SKU-E"))
	_, err := s.AddMedia(p.ID, AddMediaInput{})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("want ErrValidation for empty media body, got %v", err)
	}
}

// TestGetNotFound verifies 404 sentinel.
func TestGetNotFound(t *testing.T) {
	s := NewStore()
	_, err := s.GetByID("nonexistent-id")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestConcurrentCreate verifies that concurrent creates for the same SKU
// result in exactly one success.
func TestConcurrentCreate(t *testing.T) {
	s := NewStore()
	const n = 20
	var wg sync.WaitGroup
	successes := make(chan bool, n)

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := s.Create(CreateInput{Name: "Racer", SKU: "RACE-SKU"})
			successes <- (err == nil)
		}()
	}
	wg.Wait()
	close(successes)

	ok := 0
	for v := range successes {
		if v {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("concurrent SKU race: want exactly 1 success, got %d", ok)
	}
}
