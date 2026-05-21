// Source Asia Backend — Go 1.22
//
// Part 1: Rate-limited request API   →  POST /request, GET /stats
// Part 2: Product catalogue with media → POST /products, GET /products,
//                                        GET /products/{id}, POST /products/{id}/media
package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Tarunkiller/Source-Asia--Backend-Assignment/internal/catalog"
	"github.com/Tarunkiller/Source-Asia--Backend-Assignment/internal/ratelimit"
)

//go:embed public/index.html
var publicFS embed.FS

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()

	// ── Part 1: Rate-limited API ──────────────────────────────────────────
	limiter := ratelimit.NewSlidingWindowLimiter()
	rl := ratelimit.NewHandler(limiter)

	mux.HandleFunc("POST /request", rl.HandleRequest)
	mux.HandleFunc("GET /stats", rl.HandleStats)

	// ── Part 2: Product catalogue ─────────────────────────────────────────
	store := catalog.NewStore()
	catalog.NewHandler(store, mux) // registers all /products/* routes

	// ── Serve Dashboard ──────────────────────────────────────────────────
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		content, err := publicFS.ReadFile("public/index.html")
		if err != nil {
			http.Error(w, "Dashboard not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(content)
	})

	// ── Middleware: structured request logger ─────────────────────────────
	handler := requestLogger(mux)

	addr := ":" + port
	fmt.Printf("Source Asia backend listening on %s\n", addr)
	fmt.Println("  Part 1 → POST /request   GET /stats")
	fmt.Println("  Part 2 → POST /products  GET /products  GET /products/{id}  POST /products/{id}/media")

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// requestLogger is a thin middleware that logs method, path, status and latency.
type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func requestLogger(next http.Handler) http.Handler {
	logger := log.New(os.Stdout, "", 0)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Printf("%s  %-6s %-30s  %d  %s",
			time.Now().Format("15:04:05"),
			r.Method, r.URL.RequestURI(),
			rec.status,
			time.Since(start))
	})
}
