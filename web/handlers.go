package web

import (
	"encoding/json"
	"net/http"

	"renia/ai"
	"renia/auth"
	"renia/config"
	"renia/db"
)

// Server aggregates the domain stores and AI client required by handlers.
type Server struct {
	users         *db.UserStore
	sessions      db.SessionStore
	conversations *db.ConversationStore
	aiClient      *ai.Client
}

// NewServer wires all dependencies into a single HTTP server instance.
func NewServer(d *db.DB, aiClient *ai.Client) *Server {
	conn := d.Conn()
	return &Server{
		users:         db.NewUserStore(conn),
		sessions:      db.NewSessionStore(conn),
		conversations: db.NewConversationStore(conn),
		aiClient:      aiClient,
	}
}

// RegisterRequest is the expected body for POST /api/register.
type RegisterRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginRequest is the expected body for POST /api/login.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginResponse returns the bearer token on successful authentication.
type LoginResponse struct {
	Token string `json:"token"`
}

// ChatRequest is the expected body for POST /api/chat.
type ChatRequest struct {
	Prompt string `json:"prompt"`
}

// ChatResponse returns the assistant's generated text.
type ChatResponse struct {
	Reply string `json:"reply"`
}

// ErrorResponse standardizes JSON error payloads.
type ErrorResponse struct {
	Error string `json:"error"`
}

// createdResponse is returned on successful registration.
type createdResponse struct {
	Created bool `json:"created"`
}

// Register godoc
// @Summary      Register a new tenant account
// @Description  Creates a user with a PBKDF2-SHA256 hashed password.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body  RegisterRequest  true  "Registration credentials"
// @Success      201   {object}  createdResponse
// @Failure      400   {object}  ErrorResponse
// @Failure      409   {object}  ErrorResponse
// @Router       /api/register [post]
func (s *Server) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Username == "" || req.Password == "" {
		respondError(w, http.StatusBadRequest, "missing username or password")
		return
	}

	salt, hash, err := auth.HashPassword(req.Password)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "hash failure")
		return
	}

	_, err = s.users.CreateUser(r.Context(), req.Username, salt, hash)
	if err != nil {
		respondError(w, http.StatusConflict, "username taken")
		return
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createdResponse{Created: true})
}

// Login godoc
// @Summary      Authenticate and receive a bearer token
// @Description  Validates PBKDF2-SHA256 credentials and returns a session token.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body  body  LoginRequest  true  "Login credentials"
// @Success      200   {object}  LoginResponse
// @Failure      400   {object}  ErrorResponse
// @Failure      401   {object}  ErrorResponse
// @Router       /api/login [post]
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid json")
		return
	}

	user, err := s.users.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "lookup error")
		return
	}
	if user == nil || !auth.VerifyPassword(req.Password, user.Salt, user.PasswordHash) {
		respondError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	token, err := auth.GenerateToken()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "token generation")
		return
	}
	if err := s.sessions.CreateSession(r.Context(), token, user.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "session creation")
		return
	}

	respondJSON(w, http.StatusOK, LoginResponse{Token: token})
}

// Chat godoc
// @Summary      Submit a prompt to the BitNet RWKV inference engine
// @Description  Retrieves the last 50 lines of user-bound history, constructs a chat messages payload, forwards it to the local inference cluster, persists the turn, and returns the assistant reply.
// @Tags         chat
// @Accept       json
// @Produce      json
// @Param        body  body  ChatRequest  true  "User prompt"
// @Success      200   {object}  ChatResponse
// @Failure      400   {object}  ErrorResponse
// @Failure      401   {object}  ErrorResponse
// @Failure      502   {object}  ErrorResponse
// @Router       /api/chat [post]
func (s *Server) Chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	uid := auth.UserID(r)
	if uid == 0 {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Prompt == "" {
		respondError(w, http.StatusBadRequest, "empty prompt")
		return
	}

	history, err := s.conversations.RecentMessages(r.Context(), uid, config.HistoryLimit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "history fetch")
		return
	}

	msgs := make([]ai.ChatMessage, 0, len(history)+1)
	for _, h := range history {
		msgs = append(msgs, ai.ChatMessage{Role: h.Role, Content: h.Content})
	}
	msgs = append(msgs, ai.ChatMessage{Role: "user", Content: req.Prompt})

	reply, err := s.aiClient.Chat(r.Context(), msgs)
	if err != nil {
		respondError(w, http.StatusBadGateway, "inference failure")
		return
	}

	if err := s.conversations.AppendMessage(r.Context(), uid, "user", req.Prompt); err != nil {
		respondError(w, http.StatusInternalServerError, "persist prompt")
		return
	}
	if err := s.conversations.AppendMessage(r.Context(), uid, "assistant", reply); err != nil {
		respondError(w, http.StatusInternalServerError, "persist reply")
		return
	}

	respondJSON(w, http.StatusOK, ChatResponse{Reply: reply})
}

func respondJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func respondError(w http.ResponseWriter, code int, msg string) {
	respondJSON(w, code, ErrorResponse{Error: msg})
}
