# Source Asia Backend Assignment

A single Go 1.22 HTTP service that implements both required parts of the assignment.

> **AI tools used:** GitHub Copilot was used for boilerplate suggestions on doc-comments. All logic, architecture, and test cases were written manually.

---

## Table of contents

1. [Quick start](#quick-start)
2. [Project structure](#project-structure)
3. [Part 1 – Rate-limited API](#part-1--rate-limited-api)
4. [Part 2 – Product catalogue with media](#part-2--product-catalogue-with-media)
5. [Data model & performance](#data-model--performance)
6. [Production limitations](#production-limitations)
7. [Testing](#testing)

---

## Quick start

```bash
# Clone / unzip the repo
cd sourceasia-backend

# Run (default port 8080)
go run .

# Optional: choose a different port
PORT=9090 go run .
```

The server prints a startup banner and a structured log line for every request.

---

## Project structure

```
.
├── .gitignore             # Git ignore rules for Go and OS files
├── go.mod                 # Go module definition (aligned with repository)
├── main.go                # Server entry point (uses go:embed for dashboard)
├── render.yaml            # Render Blueprint configuration
├── README.md              # Project documentation
├── internal/
│   ├── catalog/
│   │   ├── handler.go     # Product catalog HTTP handler
│   │   ├── store.go       # In-memory product catalog database
│   │   └── store_test.go  # Product catalog unit tests
│   └── ratelimit/
│       ├── handler.go     # Rate limit request handlers
│       ├── limiter.go     # Sharded, thread-safe sliding window rate limiter
│       └── limiter_test.go# Rate limiter unit tests
└── public/
    └── index.html         # Frontend HTML Dashboard (compiled into binary)
```

---

## Part 1 – Rate-limited API

### Design choices

| Decision | Choice | Reason |
|---|---|---|
| Window type | **Rolling (sliding) window** | Fairer than a fixed window; prevents a burst of 10 requests by straddling a boundary |
| Success status | **201 Created** | The request was *accepted for processing*, analogous to queuing a job |
| Rejected status | **429 Too Many Requests** | Standard HTTP semantics; `Retry-After: 60` header is also set |
| `rejected_cumulative` | **Lifetime counter** | Easier to audit abuse patterns across many windows |
| Concurrency | Single `sync.Mutex` per operation | Correct under parallel calls; see race-detector test results |

### `POST /request`

**Request**
```json
{
  "user_id": "alice",
  "payload": { "action": "checkout", "item_id": 42 }
}
```
`payload` accepts any valid JSON value (object, array, string, number, boolean).

**Success — 201 Created**
```json
{
  "status": "accepted",
  "user_id": "alice",
  "accepted_in_window": 3,
  "window_seconds": 60
}
```

**Rate limited — 429 Too Many Requests**
```json
{
  "error": "rate_limit_exceeded",
  "message": "maximum 5 requests per 60-second rolling window reached; try again later"
}
```
Response also includes `Retry-After: 60` header.

**Bad input — 400 Bad Request**
```json
{ "error": "missing_user_id", "message": "user_id is required and must not be empty" }
```

**curl examples**
```bash
# Accepted
curl -s -X POST http://localhost:8080/request \
  -H 'Content-Type: application/json' \
  -d '{"user_id":"alice","payload":{"item":1}}'

# Trigger the limit (run 6 times rapidly)
for i in $(seq 1 6); do
  curl -s -X POST http://localhost:8080/request \
    -H 'Content-Type: application/json' \
    -d '{"user_id":"bob","payload":true}'
  echo
done

# Invalid – missing user_id
curl -s -X POST http://localhost:8080/request \
  -H 'Content-Type: application/json' \
  -d '{"payload":42}'

# Invalid – bad JSON
curl -s -X POST http://localhost:8080/request \
  -H 'Content-Type: application/json' \
  -d 'not-json'
```

---

### `GET /stats`

Returns per-user statistics. All users who have ever made a request appear here.

**Response — 200 OK**
```json
{
  "users": [
    {
      "user_id": "alice",
      "accepted_in_window": 3,
      "rejected_cumulative": 0
    },
    {
      "user_id": "bob",
      "accepted_in_window": 5,
      "rejected_cumulative": 1
    }
  ],
  "total_tracked_users": 2,
  "note": "rejected_cumulative is a lifetime counter; accepted_in_window reflects the current 60-second rolling window"
}
```

**curl example**
```bash
curl -s http://localhost:8080/stats | jq
```

---

## Part 2 – Product catalogue with media

### `POST /products`

**Request**
```json
{
  "name": "Widget A",
  "sku": "SKU-001",
  "image_urls": [
    "https://cdn.example.com/products/sku-001/img-1.jpg",
    "https://cdn.example.com/products/sku-001/img-2.jpg"
  ],
  "video_urls": [
    "https://cdn.example.com/products/sku-001/demo.mp4"
  ]
}
```
`image_urls` and `video_urls` are optional. Omitting them is the same as passing `[]`.

**Success — 201 Created** — returns the full product including the server-assigned `id`.

**Errors**

| Condition | Status |
|---|---|
| Duplicate SKU | 409 Conflict — chosen over 400 because the conflict is with an existing resource, not a malformed request |
| Empty name or SKU | 400 Bad Request |
| Invalid URL | 400 Bad Request |
| > 20 URLs in one array | 400 Bad Request |

**curl example**
```bash
curl -s -X POST http://localhost:8080/products \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "Widget A",
    "sku": "SKU-001",
    "image_urls": ["https://cdn.example.com/img1.jpg"],
    "video_urls": ["https://cdn.example.com/demo.mp4"]
  }' | jq
```

---

### `GET /products`

Returns a **paginated, lightweight** list. URL arrays are **never loaded or serialised** for list responses (see [Data model](#data-model--performance)).

**Query parameters**

| Param | Default | Max | Description |
|---|---|---|---|
| `limit` | 20 | 100 | Items per page |
| `offset` | 0 | — | Zero-based start index |

**Response — 200 OK**
```json
{
  "data": [
    {
      "id": "p_1748123456789_1",
      "name": "Widget A",
      "sku": "SKU-001",
      "image_count": 2,
      "video_count": 1,
      "thumbnail_url": "https://cdn.example.com/img1.jpg",
      "created_at": "2025-05-20T10:00:00Z"
    }
  ],
  "total": 1,
  "limit": 20,
  "offset": 0
}
```

**curl example**
```bash
curl -s "http://localhost:8080/products?limit=5&offset=0" | jq

# Seed 10 products for pagination testing
for i in $(seq 1 10); do
  curl -s -X POST http://localhost:8080/products \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"Product $i\",\"sku\":\"SKU-00$i\",
         \"image_urls\":[\"https://cdn.example.com/p$i/img1.jpg\",\"https://cdn.example.com/p$i/img2.jpg\"]}" \
    > /dev/null
done
curl -s "http://localhost:8080/products?limit=3&offset=0" | jq '.data[].name'
```

---

### `GET /products/{id}`

Returns the **full product** including all `image_urls` and `video_urls`.

**curl example**
```bash
# Replace with an id returned by POST /products
curl -s http://localhost:8080/products/p_1748123456789_1 | jq
```

**404** if the id does not exist.

---

### `POST /products/{id}/media`

Appends new URLs to an existing product. Both fields are optional, but at least one must be provided and non-empty.

**Request**
```json
{
  "image_urls": ["https://cdn.example.com/img-new.jpg"],
  "video_urls": []
}
```

**Response — 200 OK** — returns the updated full product.

**curl example**
```bash
curl -s -X POST http://localhost:8080/products/p_1748123456789_1/media \
  -H 'Content-Type: application/json' \
  -d '{"image_urls":["https://cdn.example.com/extra.jpg"]}' | jq
```

---

### Validation rules

| Rule | Detail |
|---|---|
| `name`, `sku` | Required, must not be blank or whitespace-only |
| URL scheme | Must be `http://` or `https://`; `ftp://`, relative paths, etc. are rejected |
| URL length | Maximum **2048** characters per URL (RFC 7230 practical limit) |
| URL count | Maximum **20** URLs per array **per request** (not per product; you can keep appending via `/media`) |
| SKU uniqueness | Checked under a write lock; concurrent creates for the same SKU yield exactly one success |

---

## Data model & performance

### In-memory layout

```
Store
├── products  map[id → *Product]     ← full record; image_urls + video_urls live here
├── meta      map[id → *ProductMeta] ← counts + thumbnail only; NO URL slices
├── skuIndex  map[sku → id]          ← O(1) duplicate check
└── idOrder   []string               ← insertion-ordered IDs for stable pagination
```

### List vs detail query path

| Operation | Data read | URL strings loaded |
|---|---|---|
| `GET /products?limit=20` | 20 `ProductMeta` entries | **0** |
| `GET /products/{id}` | 1 `Product` entry | all for that product |

With 1 000 products × 10 images each: a page-20 list call touches **20 meta structs** containing only integers and one string thumbnail — not 10 000 URL strings. The meta map is updated atomically alongside the product on every write.

### What would change in production (PostgreSQL + CDN)

| Concern | In-memory (current) | PostgreSQL + CDN |
|---|---|---|
| List performance | `O(page_size)` slice read | `SELECT id, name, sku, image_count, video_count, thumbnail_url … LIMIT ? OFFSET ?` on an indexed table |
| Media storage | URL strings inside the product row | Separate `product_media` table; list query does **not** join it |
| Pagination | Offset into a slice | Keyset/cursor pagination on `(created_at, id)` to avoid OFFSET drift on inserts |
| SKU uniqueness | Mutex + map | `UNIQUE` constraint on `sku` column; DB handles concurrent inserts |
| Thumbnail | `image_urls[0]` denormalised into meta | Computed column or application-maintained `thumbnail_url` column |
| CDN URLs | Stored as-is | Presigned URLs generated at read time with a short TTL |

---

## Production limitations

### Part 1 — Rate limiter

| Limitation | Notes |
|---|---|
| **Single instance only** | Rate-limit state lives in RAM; horizontal scaling requires a shared store (Redis with Lua scripting, or a distributed token-bucket service) |
| **Restart loses state** | All user buckets are lost on restart; a Redis-backed implementation persists state |
| **No user authentication** | `user_id` is a plain string; any caller can impersonate any user |
| **No bucket eviction** | Buckets for inactive users accumulate indefinitely; a real system would evict stale entries (LRU cache or TTL index) |

### Part 2 — Product catalogue

| Limitation | Notes |
|---|---|
| **In-memory only** | All data lost on restart; persistence requires a database |
| **No full-text search** | Filtering by name or category would need an inverted index (Elasticsearch, PostgreSQL `tsvector`) |
| **No update endpoint** | `PUT /products/{id}` is not in scope; would be needed for production |
| **Pagination drift** | Offset-based pagination can skip/duplicate items if products are inserted concurrently during a page scan; keyset pagination fixes this |
| **No media deduplication** | The same URL can be appended multiple times; a `SET` constraint would prevent this in a DB |

---

## Testing

```bash
# Unit tests (both packages)
go test ./...

# With race detector (verifies concurrency safety)
go test -race ./...

# Verbose output
go test -v -race ./...

# Benchmark the rate-limiter hot path
go test -bench=BenchmarkAllow -benchmem ./internal/ratelimit/
```

All 17 tests pass with `-race` enabled, confirming no data races.
