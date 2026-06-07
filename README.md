# Renia

Ultra-lightweight, CGO-free Go backend for Agentic BitNet RWKV inference with SQLite as the recurrent memory partition.

## Architecture

```
┌─────────────┐     HTTP/JSON      ┌─────────────────┐     HTTP/JSON      ┌─────────────────┐
│   Client    │ ◄────────────────► │  renia (Go)     │ ◄────────────────► │ RWKV C++ Engine │
└─────────────┘                    │  - auth         │                    │ (BitNet 1.58b)  │
                                   │  - supervisor   │                    └─────────────────┘
                                   │  - memory (DB)  │                           │
                                   └────────┬────────┘                           │
                                            │ SQLite                            │
                                   ┌────────▼────────┐                          │
                                   │ Memory Partition│◄─────────────────────────┘
                                   │  - memory_tags  │    RWKV synthesizes tool
                                   │  - memory_entries     calls to query/insert
                                   │  - conversations
                                   └─────────────────┘
```

- **Core Backend:** Go 1.22+ using `net/http` native routing (`http.NewServeMux`).
- **Database:** `modernc.org/sqlite` (100% pure Go, zero CGO). Acts as RWKV's agentic memory partition, not merely a user store.
- **Inference:** JSON-over-HTTP to a local RWKV server. Go supervises the C++ host lifecycle.
- **Agentic Loop:** RWKV receives a system prompt describing available memory tools. It may emit `TOOL_CALL: {...}` requests, which the Go backend validates and executes against the user-bound SQLite memory partition. Results are fed back to RWKV for final synthesis.

## Requirements

- Go 1.22 or newer
- CMake 3.16+
- C++17 compiler (g++ or clang++)
- ~8 GB VRAM available for the C++ inference engine
- The Go backend targets < 50 MB RAM overhead with active heap < 15 MB

## Build

The `Makefile` orchestrates the entire build:

```bash
# 1. Downloads rwkv.cpp core dependency (if absent)
# 2. Runs CMake with AVX2/NEON optimization flags
# 3. Compiles the C++ inference HTTP server
# 4. Builds the pure Go backend (CGO_ENABLED=0)
make all
```

Manual steps if preferred:
```bash
export CGO_ENABLED=0
export GOGC=20
go mod tidy
go build -ldflags="-s -w" -trimpath -o renia .
```

## Run

```bash
./renia
```

On startup, the Go backend performs the following sequence:
1. Polls `http://127.0.0.1:8080/health` to detect the C++ inference engine.
2. If absent, automatically forks and executes `./cpp/build/rwkv_server`, capturing stdout/stderr.
3. Waits for the C++ host to report healthy before binding the client-facing HTTP interface.
4. Opens `:8080` with `ReadTimeout=15s`, `WriteTimeout=60s`, `IdleTimeout=120s`.

## API Endpoints

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/register` | No | Create a new user account |
| POST | `/api/login` | No | Authenticate and receive a bearer token |
| POST | `/api/chat` | Yes | Agentic chat with memory tool loop |

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
  -d '{"prompt":"Remember that I prefer Go over Python"}'
```

Response:
```json
{"reply":"Stored your preference in the memory partition."}
```

## Database Schema

- `users` — tenant accounts with PBKDF2-SHA256 hashes.
- `sessions` — bearer tokens with automatic expiration.
- `conversations` — per-user conversation ledger (includes tool turns).
- `memory_tags` — labeled key-value pairs for fast associative retrieval.
- `memory_entries` — typed analytical memories (`fact`, `summary`, `intent`, `context`).

Indexes:
- `idx_conversations_user_created` on `(user_id, created_at DESC)`
- `idx_memory_tags_user_tag` on `(user_id, tag)`
- `idx_memory_entries_user_type` on `(user_id, entry_type)`

## Agentic Memory Tool Protocol

RWKV is instructed via system prompt that it may emit exactly one tool call per turn using the format:

```text
TOOL_CALL: {"tool":"search_memory_entries","params":{"entry_type":"fact","keyword":"Go","limit":10}}
```

Available tools:
- `search_conversations` — keyword search across the user's conversation history.
- `search_memory_tags` — pattern search across memory tags.
- `write_memory_tag` — store a labeled key-value pair.
- `search_memory_entries` — search typed memory entries by keyword.
- `write_memory_entry` — store a typed analytical memory.

The Go backend enforces:
- Maximum 5 tool iterations per chat request.
- 5-second SQLite execution timeout per tool call.
- Strict `user_id` binding on every query.

## Security

See [`VULN.md`](VULN.md) for the complete audit ledger mapping every mitigation to its source file and logic.

## Memory Optimization

- `debug.SetGCPercent(20)` in `main.go` triggers aggressive garbage collection.
- SQLite connection pool is capped to `MaxOpenConns = 1` and `MaxIdleConns = 1`.
- No in-memory caches or buffers are retained between requests.
- Request bodies are decoded and responses encoded via streaming JSON directly on `http.Request.Body` and `http.ResponseWriter`.

## Generating Swagger Docs

If you install `swag`:

```bash
go install github.com/swaggo/swag/cmd/swag@latest
swag init -g main.go
```

The annotations in `internal/web/server.go` and `main.go` produce an OpenAPI 3.0-compatible spec.

## License

MIT
