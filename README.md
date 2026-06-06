# Renia

Ultra-lightweight, CGO-free Go backend for local BitNet RWKV inference.

## Architecture

```
┌─────────────┐     HTTP/JSON      ┌─────────────────┐
│   Client    │ ◄────────────────► │  renia (Go)     │
└─────────────┘                    │  - auth         │
                                   │  - db (SQLite)  │
                                   │  - ai client    │
                                   └────────┬────────┘
                                            │ HTTP/JSON
                                   ┌────────▼────────┐
                                   │ RWKV C++ Engine │
                                   │ (BitNet 1.58b)  │
                                   └─────────────────┘
```

- **Core Backend:** Go 1.22+ using `net/http` native routing (`http.NewServeMux`).
- **Database:** `modernc.org/sqlite` (100% pure Go, zero CGO).
- **Inference:** JSON-over-HTTP client to a local RWKV server (default `http://127.0.0.1:8080/v1/chat/completions`).

## Requirements

- Go 1.22 or newer
- ~8 GB VRAM available for the C++ inference engine
- The Go backend targets < 50 MB RAM overhead with active heap < 15 MB

## Build

```bash
# Ensure pure Go build
export CGO_ENABLED=0
export GOGC=20

# Download dependencies
go mod tidy

# Build production binary
go build -ldflags="-s -w" -trimpath -o renia .
```

## Run

```bash
CGO_ENABLED=0 GOGC=20 ./renia
```

The server listens on `:8080` with `ReadTimeout=15s`, `WriteTimeout=60s`, `IdleTimeout=120s`.

## API Endpoints

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/register` | No | Create a new user account |
| POST | `/api/login` | No | Authenticate and receive a bearer token |
| POST | `/api/chat` | Yes | Send a prompt and receive an assistant reply |

### Register

```bash
curl -X POST http://localhost:8080/api/register \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"secret123"}'
```

### Login

```bash
curl -X POST http://localhost:8080/api/login \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"secret123"}'
```

Response:
```json
{"token":"a1b2c3..."}
```

### Chat

```bash
curl -X POST http://localhost:8080/api/chat \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer a1b2c3..." \
  -d '{"prompt":"Explain recursion"}'
```

Response:
```json
{"reply":"Recursion is a process where a function calls itself..."}
```

## Database Schema

- `users` — tenant accounts with PBKDF2-SHA256 hashes.
- `conversations` — per-user message history with FK to `users(id)`.
- `sessions` — bearer tokens with automatic expiration.

Index: `idx_conversations_user_created` on `(user_id, created_at DESC)`.

## Security

See [`VULN.md`](VULN.md) for the complete audit ledger mapping every mitigation to its source file and logic.

## Memory Optimization

- `debug.SetGCPercent(20)` in `main.go` triggers aggressive garbage collection.
- SQLite connection pool is capped to `MaxOpenConns = 1` and `MaxIdleConns = 1`.
- Chat history is limited to the most recent 50 lines per user.
- No in-memory caches or buffers are retained between requests.
- Request bodies are decoded and responses encoded via streaming JSON directly on `http.Request.Body` and `http.ResponseWriter`.

## Generating Swagger Docs

If you install `swag`:

```bash
go install github.com/swaggo/swag/cmd/swag@latest
swag init -g main.go
```

The annotations in `web/handlers.go` and `main.go` produce an OpenAPI 3.0-compatible spec.

## License

MIT
