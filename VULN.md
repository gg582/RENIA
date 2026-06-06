# Renia Security Audit Ledger

This document maps every critical attack vector addressed in the renia codebase to its precise mitigation location.

---

## 1. Timing Attacks on Password Verification

**Vector:** Response-time analysis reveals password validity through early-exit comparison.

**Mitigation:** `auth/password.go`, function `VerifyPassword` (lines 40–48).
- Derives the PBKDF2 key for every invocation.
- Compares the derived key against the stored hash using `crypto/subtle.ConstantTimeCompare`.
- No branching on match/mismatch before the comparison completes.

---

## 2. Weak Password Hashing Parameters

**Vector:** Insufficient key stretching enables offline hash cracking.

**Mitigation:** `auth/password.go`, function `HashPassword` (lines 26–33).
- Uses `golang.org/x/crypto/pbkdf2` with `crypto/sha256`.
- Iteration count defined in `config/config.go` as `PBKDF2Iterations = 600000`, satisfying OWASP PBKDF2-SHA256 guidance.
- Salt length is 32 bytes drawn from `crypto/rand`.

---

## 3. SQL Injection

**Vector:** Untrusted input is concatenated into SQL, enabling exfiltration or tampering.

**Mitigation:**
- `db/user.go`, functions `CreateUser` and `GetUserByUsername` (lines 33–57).
- `db/conversation.go`, functions `AppendMessage` and `RecentMessages` (lines 37–67).
- `db/session.go`, functions `CreateSession` and `ResolveToken` (lines 37–64).
- All queries use parameterized placeholders (`?`) with `database/sql` driver binding.

---

## 4. Multi-Tenant Data Leakage

**Vector:** Authenticated user A manipulates a request to access user B’s conversation history.

**Mitigation:**
- `db/conversation.go`, function `RecentMessages` (lines 52–67): hard-bound `user_id = ?`.
- `db/conversation.go`, function `AppendMessage` (lines 37–46): hard-bound `user_id = ?` insertion.
- `web/handlers.go`, function `Chat` (lines 152–218):
  - Extracts `user_id` exclusively from the request context injected by `LoggingAndAuthMiddleware`.
  - Never reads `user_id` from the JSON body.

---

## 5. Authentication Bypass / Session Forgery

**Vector:** Crafted or guessed session tokens impersonate other tenants.

**Mitigation:**
- `auth/token.go`, function `GenerateToken` (lines 16–23): 32-byte `crypto/rand` tokens.
- `web/server.go`, function `LoggingAndAuthMiddleware` (lines 30–50):
  - Rejects missing or invalid `Authorization` headers.
  - Resolves tokens against the `sessions` table with expiration check.
  - Injects `user_id` into the request context via `auth.WithUserID`.

---

## 6. CGO Dependency Risk

**Vector:** CGO-enabled drivers introduce C toolchain dependencies and memory-unsafe surface.

**Mitigation:**
- `db/db.go`, import block (line 10): `_ "modernc.org/sqlite"`.
- `go.mod` dependency: `modernc.org/sqlite` (pure Go).
- The project builds with `CGO_ENABLED=0`.

---

## 7. Denial-of-Service via Unbounded Request Body

**Vector:** Enormous JSON payloads exhaust RAM before processing.

**Mitigation:**
- `web/handlers.go`, handlers `Register`, `Login`, and `Chat` (lines 76–218).
- All decode directly into bounded structs using `json.NewDecoder(r.Body).Decode(&req)`.
- No unbounded `[]byte` or `map[string]interface{}` accumulation.

---

## 8. Inference Engine Timeout & Resource Exhaustion

**Vector:** The local RWKV process hangs, causing goroutine and memory accumulation.

**Mitigation:**
- `ai/client.go`, struct `Client` (lines 20–24): HTTP client timeout set to `config.AITimeout = 45s`.
- `ai/client.go`, function `Chat` (lines 33–65):
  - Uses `http.NewRequestWithContext(ctx, ...)` for deadline propagation.
  - Returns structured errors on timeout without panicking.

---

## 9. Information Disclosure via Error Messages

**Vector:** Verbose internal errors leak schema details or file paths to clients.

**Mitigation:**
- `web/handlers.go`, helper `respondError` (lines 226–228): returns generic strings.
- Internal `fmt.Errorf` chains in `db/`, `ai/`, and `auth/` are never serialized into HTTP response bodies.

---

## 10. Cross-User Session Fixation / Stale Session Accumulation

**Vector:** Expired sessions remain indefinitely, enlarging brute-force surface.

**Mitigation:**
- `db/schema.go` (lines 24–26): `sessions.expires_at` is mandatory (`NOT NULL`).
- `db/session.go`, function `GC` (lines 75–81): `DELETE FROM sessions WHERE expires_at <= datetime('now')`.
- Production deployments should invoke `GC` on a periodic background ticker.
