package web

import (
	"log"
	"net/http"
	"strings"
	"time"

	"renia/ai"
	"renia/auth"
	"renia/db"
)

// responseRecorder captures the status code for access logging.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

// LoggingAndAuthMiddleware wraps the mux with request logging and
// bearer-token authentication for protected paths.
func LoggingAndAuthMiddleware(mux *http.ServeMux, database *db.DB) http.Handler {
	sessions := db.NewSessionStore(database.Conn())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rr := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		if strings.HasPrefix(r.URL.Path, "/api/chat") {
			token := r.Header.Get("Authorization")
			if len(token) > 7 && strings.EqualFold(token[:7], "Bearer ") {
				token = token[7:]
			}
			uid, err := sessions.ResolveToken(r.Context(), token)
			if err != nil || uid == 0 {
				respondError(rr, http.StatusUnauthorized, "unauthorized")
				log.Printf("%s %s %d %v", r.Method, r.URL.Path, http.StatusUnauthorized, time.Since(start))
				return
			}
			r = r.WithContext(auth.WithUserID(r.Context(), uid))
		}

		mux.ServeHTTP(rr, r)

		log.Printf("%s %s %d %v", r.Method, r.URL.Path, rr.statusCode, time.Since(start))
	})
}

// NewRouter assembles the http.ServeMux with all application routes.
func NewRouter(database *db.DB, aiClient *ai.Client) *http.ServeMux {
	srv := NewServer(database, aiClient)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/register", srv.Register)
	mux.HandleFunc("/api/login", srv.Login)
	mux.HandleFunc("/api/chat", srv.Chat)
	return mux
}
