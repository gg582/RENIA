# Renia Security Audit Ledger

This document maps every critical attack vector addressed in the renia codebase to its precise mitigation location.

---

## 1. Timing Attacks on Password Verification

**Vector:** Response-time analysis reveals password validity through early-exit comparison.

**Mitigation:** `internal/auth/pbkdf2.go`, function `VerifyPassword`.
- Derives the PBKDF2 key for every invocation.
- Compares the derived key against the stored hash using `crypto/subtle.ConstantTimeCompare`.
- No branching on match/mismatch before the comparison completes.

---

## 2. Weak Password Hashing Parameters

**Vector:** Insufficient key stretching enables offline hash cracking.

**Mitigation:** `internal/auth/pbkdf2.go`, function `HashPassword`.
- Uses `golang.org/x/crypto/pbkdf2` with `crypto/sha256`.
- Iteration count defined in `config/config.go` as `PBKDF2Iterations = 600000`, satisfying OWASP PBKDF2-SHA256 guidance.
- Salt length is 32 bytes drawn from `crypto/rand`.

---

## 3. SQL Injection

**Vector:** Untrusted input is concatenated into SQL, enabling exfiltration or tampering.

**Mitigation:**
- `internal/database/sqlite.go`, functions `CreateUser`, `GetUserByUsername`, `CreateSession`, `ResolveToken`.
- `internal/database/memory.go`, functions `AppendConversation`, `RecentConversations`, `SearchConversations`, `WriteMemoryTag`, `SearchMemoryTags`, `WriteMemoryEntry`, `SearchMemoryEntries`, `ExecuteTool`.
- All queries use parameterized placeholders (`?`) with `database/sql` driver binding.
- RWKV never emits raw SQL; it emits structured tool names and parameters that are validated and bound.

---

## 4. Multi-Tenant Data Leakage

**Vector:** Authenticated user A manipulates a request to access user B's memory partition.

**Mitigation:**
- `internal/database/memory.go`: every function hard-binds `user_id = ?`.
- `internal/web/server.go`, function `Chat`:
  - Extracts `user_id` exclusively from the request context injected by `LoggingAndAuthMiddleware`.
  - Never reads `user_id` from the JSON body.
  - Passes the context-bound `uid` into `ExecuteTool`, preventing cross-user memory access.

---

## 5. Authentication Bypass / Session Forgery

**Vector:** Crafted or guessed session tokens impersonate other tenants.

**Mitigation:**
- `internal/auth/pbkdf2.go`, function `GenerateToken`: 32-byte `crypto/rand` tokens.
- `internal/web/server.go`, function `LoggingAndAuthMiddleware`:
  - Rejects missing or invalid `Authorization` headers.
  - Resolves tokens against the `sessions` table with expiration check.
  - Injects `user_id` into the request context via `auth.WithUserID`.

---

## 6. CGO Dependency Risk

**Vector:** CGO-enabled drivers introduce C toolchain dependencies and memory-unsafe surface.

**Mitigation:**
- `internal/database/sqlite.go`, import block: `_ "modernc.org/sqlite"`.
- `go.mod` dependency: `modernc.org/sqlite` (pure Go).
- The project builds with `CGO_ENABLED=0`.

---

## 7. Denial-of-Service via Unbounded Request Body

**Vector:** Enormous JSON payloads exhaust RAM before processing.

**Mitigation:**
- `internal/web/server.go`, handlers `Register`, `Login`, and `Chat`.
- All decode directly into bounded structs using `json.NewDecoder(r.Body).Decode(&req)`.
- No unbounded `[]byte` or `map[string]interface{}` accumulation.

---

## 8. Inference Engine Timeout & Resource Exhaustion

**Vector:** The local RWKV process hangs, causing goroutine and memory accumulation.

**Mitigation:**
- `internal/ai/supervisor.go`, struct `Supervisor`: HTTP client timeout set to 45s.
- `internal/ai/supervisor.go`, function `Chat`:
  - Uses `http.NewRequestWithContext(ctx, ...)` for deadline propagation.
  - Returns structured errors on timeout without panicking.
- `internal/ai/supervisor.go`, function `EnsureRunning`:
  - Auto-starts the C++ host if missing.
  - Enforces `startupTimeout` (30s) on health checks.

---

## 9. Tool Execution Abuse

**Vector:** RWKV or a malicious client triggers expensive or infinite database operations through the agentic tool loop.

**Mitigation:**
- `internal/database/memory.go`, function `ExecuteTool`:
  - Enforces a 5-second context timeout per tool call to prevent SQLite single-writer lock starvation.
  - Whitelists only known tool names; unknown tools return an error immediately.
  - Validates `limit` parameters to a maximum of 50 rows.
- `internal/web/server.go`, function `Chat`:
  - Caps the agentic tool loop to `maxToolIterations = 5`.

---

## 10. Information Disclosure via Error Messages

**Vector:** Verbose internal errors leak schema details or file paths to clients.

**Mitigation:**
- `internal/web/server.go`, helper `respondError`: returns generic strings.
- Internal `fmt.Errorf` chains in `internal/database/`, `internal/ai/`, and `internal/auth/` are never serialized into HTTP response bodies.

---

## 11. Cross-User Session Fixation / Stale Session Accumulation

**Vector:** Expired sessions remain indefinitely, enlarging brute-force surface.

**Mitigation:**
- `internal/database/sqlite.go`, schema: `sessions.expires_at` is mandatory (`NOT NULL`).
- `internal/database/sqlite.go`, function `GCSessions`: `DELETE FROM sessions WHERE expires_at <= datetime('now')`.
- Production deployments should invoke `GCSessions` on a periodic background ticker.
