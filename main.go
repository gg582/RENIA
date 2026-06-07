package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/a-h/templ"
)

const (
	listenAddr = ":8080"
	dbPath     = "./renia.db"
)

//go:embed static
var staticEmbed embed.FS

func staticHandler() http.Handler {
	fsys, err := fs.Sub(staticEmbed, "static")
	if err != nil {
		panic(err)
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(fsys)))
}

// Server aggregates dependencies for HTTP handlers.
type Server struct {
	db *DB
	ai *Supervisor
}

// Request/response types.
type RegisterRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token string `json:"token"`
}

type ChatRequest struct {
	Prompt    string `json:"prompt"`
	SessionID *int64 `json:"session_id,omitempty"`
}

type ChatResponse struct {
	Reply          string       `json:"reply"`
	SessionID      int64        `json:"session_id"`
	PendingTool    *toolCall    `json:"pending_tool,omitempty"`
	PendingChanges []FileChange `json:"pending_changes,omitempty"`
	PendingShell   []string     `json:"pending_shell,omitempty"`
}

type ApproveToolRequest struct {
	SessionID int64    `json:"session_id"`
	Tool      toolCall `json:"tool"`
}

type ApplyChangesRequest struct {
	SessionID int64        `json:"session_id"`
	Changes   []FileChange `json:"changes"`
	Shell     []string     `json:"shell"`
}

type CreateSessionRequest struct {
	Title       string `json:"title"`
	WorkspaceID *int64 `json:"workspace_id,omitempty"`
}

type RenameSessionRequest struct {
	Title string `json:"title"`
}

type CreateWorkspaceRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type SetModeRequest struct {
	Mode string `json:"mode"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type createdResponse struct {
	Created bool `json:"created"`
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

// LoggingAndAuthMiddleware wraps the mux with access logging and bearer-token auth.
func LoggingAndAuthMiddleware(mux *http.ServeMux, db *DB) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rr := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		if strings.HasPrefix(r.URL.Path, "/api/chat") || strings.HasPrefix(r.URL.Path, "/api/sessions") || strings.HasPrefix(r.URL.Path, "/api/workspaces") || strings.HasPrefix(r.URL.Path, "/api/mode") || strings.HasPrefix(r.URL.Path, "/api/approve-tool") || strings.HasPrefix(r.URL.Path, "/api/apply-changes") {
			token := r.Header.Get("Authorization")
			if len(token) > 7 && strings.EqualFold(token[:7], "Bearer ") {
				token = token[7:]
			}
			uid, err := db.resolveToken(r.Context(), token)
			if err != nil || uid == 0 {
				respondError(rr, http.StatusUnauthorized, "unauthorized")
				log.Printf("%s %s %d %v", r.Method, r.URL.Path, http.StatusUnauthorized, time.Since(start))
				return
			}
			r = r.WithContext(withUserID(r.Context(), uid))
		}

		mux.ServeHTTP(rr, r)
		log.Printf("%s %s %d %v", r.Method, r.URL.Path, rr.statusCode, time.Since(start))
	})
}

// UI handlers.
func (s *Server) Index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) LoginPage(w http.ResponseWriter, r *http.Request) {
	templ.Handler(LoginPage()).ServeHTTP(w, r)
}

func (s *Server) ChatPage(w http.ResponseWriter, r *http.Request) {
	templ.Handler(ChatPage()).ServeHTTP(w, r)
}

func buildSystemPrompt() string {
	return `You are RENIA, a helpful coding assistant. Reply in the same language the user writes in.
You have access to file tools, memory tools, and embedded code blocks.

When you need to read files, list directories, run commands, or use memory, emit exactly one tool call like this:
TOOL_CALL: {"tool":"read_file","params":{"path":"relative/path"}}

Available file tools: read_file, write_file, str_replace_file, list_dir, execute_command.
Available memory tools: search_conversations, search_memory_tags, write_memory_tag, search_memory_entries, write_memory_entry.

When you need to write or modify files without using TOOL_CALL, you may embed changes directly in your reply using markdown code blocks:

1. Full file write:
` + "```" + `relative/path/to/file.go
package main
...
` + "```" + `

2. Search/Replace block:
` + "```" + `relative/path/to/file.go
<<<<<<< SEARCH
old code
=======
new code
>>>>>>> REPLACE
` + "```" + `

3. Unified diff:
` + "```" + `diff
--- a/relative/path/to/file.go
+++ b/relative/path/to/file.go
@@ -1,3 +1,3 @@
...
` + "```" + `

When you need to run shell commands without using TOOL_CALL:
` + "```" + `bash
command here
` + "```" + `

The system will detect and apply embedded code blocks automatically.
Only call a tool when it is actually needed. For greetings or simple questions, just reply naturally.`
}

func (s *Server) commitUserPreferences(ctx context.Context, uid, sessionID int64) {
	msgs, err := s.db.getChatSessionMessages(ctx, uid, sessionID, 10)
	if err != nil || len(msgs) < 2 {
		return
	}

	// 1. Load existing user_profile and preference entries
	existingTags, _ := s.db.searchMemoryTags(ctx, uid, "user_profile")
	existingEntries, _ := s.db.searchMemoryEntries(ctx, uid, "preference", "", 50)

	var existing strings.Builder
	existing.WriteString("=== 기존에 파악된 사용자 기호 ===\n")
	for _, t := range existingTags {
		existing.WriteString(fmt.Sprintf("- %s: %s\n", t.Tag, t.Value))
	}
	for _, e := range existingEntries {
		existing.WriteString(fmt.Sprintf("- [%s] %s\n", e.EntryType, e.Content))
	}
	if existing.Len() == len("=== 기존에 파악된 사용자 기호 ===\n") {
		existing.WriteString("(없음)\n")
	}

	// 2. Recent conversation
	var conv strings.Builder
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		conv.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
	}

	// 3. Ask AI to synthesize a consolidated profile
	prompt := fmt.Sprintf(
		"%s\n=== 최근 대화 ===\n%s\n\n"+
		"위 기존 기호와 최근 대화를 종합하여, 중복 없이 하나의 통합된 사용자 프로필을 작성하세요. "+
		"기존 기호가 잘못되었으면 수정하고, 새 기호가 있으면 추가하세요. "+
		"반드시 write_memory_tag를 호출하여 tag='user_profile'에 통합 프로필을 저장하세요. "+
		"변경이 없으면 'NO_CHANGE'라고만 답하세요.",
		existing.String(), conv.String(),
	)

	extractMsgs := []chatMessage{
		{Role: "system", Content: "You are a profile synthesis assistant. Consolidate all known user preferences into a single concise profile. Reply in the same language the user uses."},
		{Role: "user", Content: prompt},
	}

	reply, err := s.ai.Chat(ctx, extractMsgs)
	if err != nil {
		return
	}
	if strings.TrimSpace(reply) == "NO_CHANGE" {
		return
	}

	// Try to parse as tool call first; if not, store raw text directly
	call, ok := parseToolCall(reply)
	if ok && call.Tool == "write_memory_tag" {
		tag, _ := call.Params["tag"].(string)
		value, _ := call.Params["value"].(string)
		importance, _ := call.Params["importance"].(float64)
		if importance == 0 {
			importance = 1.0
		}
		if tag == "user_profile" {
			_ = s.db.replaceMemoryTag(ctx, uid, tag, value, importance)
			return
		}
	}
	// Fallback: store raw reply as user_profile
	if len(strings.TrimSpace(reply)) > 0 {
		_ = s.db.replaceMemoryTag(ctx, uid, "user_profile", reply, 1.0)
	}
}

func (s *Server) summarizeSessionConversation(ctx context.Context, uid, sessionID int64) error {
	msgs, err := s.db.getChatSessionMessages(ctx, uid, sessionID, 200)
	if err != nil || len(msgs) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("Summarize the user's key preferences, intents, and important facts revealed in this conversation. Be concise and factual.\n\nConversation:\n")
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
	}

	messages := []chatMessage{
		{Role: "system", Content: "You are a memory extraction assistant. Extract only concrete preferences and facts."},
		{Role: "user", Content: sb.String()},
	}

	reply, err := s.ai.Chat(ctx, messages)
	if err != nil {
		return err
	}
	return s.db.writeMemoryEntry(ctx, uid, "summary", reply, 0.9)
}

func (s *Server) getWorkspacePath(ctx context.Context, uid, sessionID int64) string {
	session, err := s.db.getChatSession(ctx, uid, sessionID)
	if err != nil || session == nil || session.WorkspaceID == nil {
		return ""
	}
	ws, err := s.db.getWorkspaceByID(ctx, uid, *session.WorkspaceID)
	if err != nil || ws == nil {
		return ""
	}
	return ws.Path
}

func (s *Server) appendAndRunLoop(ctx context.Context, w http.ResponseWriter, uid, sessionID int64, messages []chatMessage, workspacePath, userMode string) {
	const maxToolIterations = 5
	for i := 0; i < maxToolIterations; i++ {
		reply, err := s.ai.Chat(ctx, messages)
		if err != nil {
			respondError(w, http.StatusBadGateway, "inference failure")
			return
		}

		call, ok := parseToolCall(reply)
		if !ok {
			// --- NEW: smart apply pipeline ---
			changes := ExtractCodeChanges(reply)
			commands := ExtractShellCommands(reply)
			if len(changes) > 0 || len(commands) > 0 {
				if workspacePath == "" {
					toolResult := `{"error":"no workspace assigned to this session"}`
					messages = append(messages, chatMessage{Role: "assistant", Content: reply}, chatMessage{Role: "tool", Content: toolResult})
					continue
				}
				if userMode == "approval" {
					_ = s.db.appendConversation(ctx, uid, sessionID, "assistant", reply, "")
					respondJSON(w, http.StatusOK, ChatResponse{
						Reply:          reply,
						SessionID:      sessionID,
						PendingChanges: changes,
						PendingShell:   commands,
					})
					return
				}
				// YOLO: apply immediately
				engine := NewWorkspaceEngine(s.db, uid, sessionID, workspacePath)
				res, _ := engine.applyChanges(changes)
				shellRes, _ := engine.runShellCommands(commands)
				var combined strings.Builder
				if res != "" {
					combined.WriteString("File changes:\n")
					combined.WriteString(res)
				}
				if shellRes != "" {
					if combined.Len() > 0 {
						combined.WriteString("\n\n")
					}
					combined.WriteString("Shell output:\n")
					combined.WriteString(shellRes)
				}
				toolResult := combined.String()
				if toolResult == "" {
					toolResult = "No changes or commands applied."
				}
				messages = append(messages, chatMessage{Role: "assistant", Content: reply}, chatMessage{Role: "tool", Content: toolResult})
				continue
			}
			// --- END NEW ---

			_ = s.db.appendConversation(ctx, uid, sessionID, "assistant", reply, "")
			respondJSON(w, http.StatusOK, ChatResponse{Reply: reply, SessionID: sessionID})
			go s.commitUserPreferences(context.Background(), uid, sessionID)
			return
		}

		if isFileTool(call.Tool) {
			if workspacePath == "" {
				toolResult := `{"error":"no workspace assigned to this session"}`
				messages = append(messages, chatMessage{Role: "assistant", Content: reply}, chatMessage{Role: "tool", Content: toolResult})
				continue
			}
			if userMode == "approval" {
				_ = s.db.appendConversation(ctx, uid, sessionID, "assistant", reply, "")
				respondJSON(w, http.StatusOK, ChatResponse{
					Reply:       "I'd like to execute: **" + call.Tool + "**. Please approve to continue.",
					SessionID:   sessionID,
					PendingTool: &call,
				})
				return
			}
			engine := NewWorkspaceEngine(s.db, uid, sessionID, workspacePath)
			res, err := engine.runTool(call)
			if err != nil {
				res = `{"error":"` + err.Error() + `"}`
			}
			messages = append(messages, chatMessage{Role: "assistant", Content: reply}, chatMessage{Role: "tool", Content: res})
		} else {
			toolResult, err := s.db.executeTool(ctx, uid, call)
			if err != nil {
				toolResult = `{"error":"` + err.Error() + `"}`
			}
			messages = append(messages, chatMessage{Role: "assistant", Content: reply}, chatMessage{Role: "tool", Content: toolResult})
		}
	}
	respondError(w, http.StatusBadGateway, "too many tool iterations")
}

// Chat godoc
func (s *Server) Chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	uid := userID(r)
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

	u, err := s.db.getUserByID(r.Context(), uid)
	if err != nil || u == nil {
		respondError(w, http.StatusInternalServerError, "user lookup error")
		return
	}

	// Ensure session exists.
	var sessionID int64
	if req.SessionID != nil && *req.SessionID > 0 {
		sessionID = *req.SessionID
	} else {
		title := req.Prompt
		if len(title) > 30 {
			title = title[:30] + "..."
		}
		sessionID, err = s.db.createChatSession(r.Context(), uid, nil, title)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "session creation failed")
			return
		}
	}

	workspacePath := s.getWorkspacePath(r.Context(), uid, sessionID)

	systemPrompt := buildSystemPrompt()
	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
	}

	// Load prior messages for this session.
	prior, err := s.db.getChatSessionMessages(r.Context(), uid, sessionID, 50)
	if err == nil {
		for _, m := range prior {
			if m.Role == "user" || m.Role == "assistant" || m.Role == "tool" {
				messages = append(messages, chatMessage{Role: m.Role, Content: m.Content})
			}
		}
	}

	messages = append(messages, chatMessage{Role: "user", Content: req.Prompt})
	_ = s.db.appendConversation(r.Context(), uid, sessionID, "user", req.Prompt, "")

	s.appendAndRunLoop(r.Context(), w, uid, sessionID, messages, workspacePath, u.Mode)
}

// ApproveTool executes a pending file tool after user approval.
func (s *Server) ApproveTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	uid := userID(r)
	if uid == 0 {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req ApproveToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.SessionID == 0 {
		respondError(w, http.StatusBadRequest, "missing session_id")
		return
	}

	session, err := s.db.getChatSession(r.Context(), uid, req.SessionID)
	if err != nil || session == nil {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}

	workspacePath := s.getWorkspacePath(r.Context(), uid, req.SessionID)
	if workspacePath == "" && isFileTool(req.Tool.Tool) {
		respondError(w, http.StatusBadRequest, "no workspace for this session")
		return
	}

	// Execute tool
	engine := NewWorkspaceEngine(s.db, uid, req.SessionID, workspacePath)
	result, err := engine.runTool(req.Tool)
	if err != nil {
		result = `{"error":"` + err.Error() + `"}`
	}

	// Record tool call and result
	toolJSON, _ := json.Marshal(req.Tool)
	_ = s.db.appendConversation(r.Context(), uid, req.SessionID, "assistant", "TOOL_CALL: "+string(toolJSON), "")
	_ = s.db.appendConversation(r.Context(), uid, req.SessionID, "tool", result, "")

	// Build messages and continue loop
	messages := []chatMessage{{Role: "system", Content: buildSystemPrompt()}}
	prior, _ := s.db.getChatSessionMessages(r.Context(), uid, req.SessionID, 50)
	for _, m := range prior {
		if m.Role == "user" || m.Role == "assistant" || m.Role == "tool" {
			messages = append(messages, chatMessage{Role: m.Role, Content: m.Content})
		}
	}

	u, _ := s.db.getUserByID(r.Context(), uid)
	mode := "approval"
	if u != nil {
		mode = u.Mode
	}
	s.appendAndRunLoop(r.Context(), w, uid, req.SessionID, messages, workspacePath, mode)
}

// ApplyChanges executes pending code changes + shell commands after user approval.
func (s *Server) ApplyChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	uid := userID(r)
	if uid == 0 {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req ApplyChangesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.SessionID == 0 {
		respondError(w, http.StatusBadRequest, "missing session_id")
		return
	}

	session, err := s.db.getChatSession(r.Context(), uid, req.SessionID)
	if err != nil || session == nil {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}

	workspacePath := s.getWorkspacePath(r.Context(), uid, req.SessionID)
	if workspacePath == "" {
		respondError(w, http.StatusBadRequest, "no workspace for this session")
		return
	}

	engine := NewWorkspaceEngine(s.db, uid, req.SessionID, workspacePath)
	res, _ := engine.applyChanges(req.Changes)
	shellRes, _ := engine.runShellCommands(req.Shell)

	var combined strings.Builder
	if res != "" {
		combined.WriteString("File changes:\n")
		combined.WriteString(res)
	}
	if shellRes != "" {
		if combined.Len() > 0 {
			combined.WriteString("\n\n")
		}
		combined.WriteString("Shell output:\n")
		combined.WriteString(shellRes)
	}
	toolResult := combined.String()
	if toolResult == "" {
		toolResult = "No changes or commands applied."
	}

	// Record assistant reply (reconstruct from DB last assistant msg) and tool result
	_ = s.db.appendConversation(r.Context(), uid, req.SessionID, "tool", toolResult, "")

	// Build messages and continue loop
	messages := []chatMessage{{Role: "system", Content: buildSystemPrompt()}}
	prior, _ := s.db.getChatSessionMessages(r.Context(), uid, req.SessionID, 50)
	for _, m := range prior {
		if m.Role == "user" || m.Role == "assistant" || m.Role == "tool" {
			messages = append(messages, chatMessage{Role: m.Role, Content: m.Content})
		}
	}

	u, _ := s.db.getUserByID(r.Context(), uid)
	mode := "approval"
	if u != nil {
		mode = u.Mode
	}
	s.appendAndRunLoop(r.Context(), w, uid, req.SessionID, messages, workspacePath, mode)
}

// Workspaces handler.
func (s *Server) Workspaces(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	if uid == 0 {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		ws, err := s.db.getWorkspaces(r.Context(), uid)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		respondJSON(w, http.StatusOK, ws)
	case http.MethodPost:
		var req CreateWorkspaceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.Name == "" || req.Path == "" {
			respondError(w, http.StatusBadRequest, "missing name or path")
			return
		}
		id, err := s.db.createWorkspace(r.Context(), uid, req.Name, req.Path)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		respondJSON(w, http.StatusOK, map[string]int64{"id": id})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// WorkspaceDetail handler.
func (s *Server) WorkspaceDetail(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	if uid == 0 {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		respondError(w, http.StatusBadRequest, "invalid path")
		return
	}
	workspaceID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid workspace id")
		return
	}

	if r.Method == http.MethodDelete {
		if err := s.db.deleteWorkspace(r.Context(), uid, workspaceID); err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"deleted": true})
		return
	}

	respondError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// Mode handler.
func (s *Server) Mode(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	if uid == 0 {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		u, err := s.db.getUserByID(r.Context(), uid)
		if err != nil || u == nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"mode": u.Mode})
	case http.MethodPost:
		var req SetModeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.Mode != "approval" && req.Mode != "yolo" {
			respondError(w, http.StatusBadRequest, "mode must be approval or yolo")
			return
		}
		if err := s.db.setUserMode(r.Context(), uid, req.Mode); err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		respondJSON(w, http.StatusOK, map[string]string{"mode": req.Mode})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Sessions handler (list / create).
func (s *Server) Sessions(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	if uid == 0 {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		sessions, err := s.db.getChatSessions(r.Context(), uid)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		respondJSON(w, http.StatusOK, sessions)

	case http.MethodPost:
		var req CreateSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid json")
			return
		}
		id, err := s.db.createChatSession(r.Context(), uid, req.WorkspaceID, req.Title)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		respondJSON(w, http.StatusOK, map[string]int64{"id": id})

	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// SessionDetail handler (delete / rename / get messages).
func (s *Server) SessionDetail(w http.ResponseWriter, r *http.Request) {
	uid := userID(r)
	if uid == 0 {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		respondError(w, http.StatusBadRequest, "invalid path")
		return
	}
	sessionID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid session id")
		return
	}

	var sub string
	if len(parts) >= 4 {
		sub = parts[3]
	}

	if r.Method == http.MethodDelete && sub == "" {
		_ = s.compressSessionMemory(r.Context(), uid, sessionID)
		_ = s.summarizeSessionConversation(r.Context(), uid, sessionID)
		if err := s.db.deleteChatSession(r.Context(), uid, sessionID); err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"deleted": true})
		return
	}

	if r.Method == http.MethodPost && sub == "rename" {
		var req RenameSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if req.Title == "" {
			respondError(w, http.StatusBadRequest, "empty title")
			return
		}
		if err := s.db.updateChatSessionTitle(r.Context(), uid, sessionID, req.Title); err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}

	if r.Method == http.MethodGet && sub == "messages" {
		msgs, err := s.db.getChatSessionMessages(r.Context(), uid, sessionID, 200)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "db error")
			return
		}
		respondJSON(w, http.StatusOK, msgs)
		return
	}

	respondError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (s *Server) compressSessionMemory(ctx context.Context, uid, sessionID int64) error {
	snaps, err := s.db.getFileSnapshots(ctx, sessionID)
	if err != nil || len(snaps) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("Summarize the following code changes made in this session. Be concise (2-3 sentences per file).\n\n")
	for _, snap := range snaps {
		sb.WriteString(fmt.Sprintf("File: %s\nAction: %s\nContent:\n%s\n\n", snap.FilePath, snap.Action, snap.Content))
	}

	messages := []chatMessage{
		{Role: "system", Content: "You are a concise coding assistant. Summarize code changes. Reply in the same language as the user."},
		{Role: "user", Content: sb.String()},
	}

	reply, err := s.ai.Chat(ctx, messages)
	if err != nil {
		return err
	}
	return s.db.writeMemoryEntry(ctx, uid, "code_summary", reply, 1.0)
}

func respondJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func respondError(w http.ResponseWriter, code int, msg string) {
	respondJSON(w, code, ErrorResponse{Error: msg})
}

// NewRouter assembles the application routes.
func NewRouter(srv *Server) *http.ServeMux {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/register", srv.Register)
	mux.HandleFunc("/api/login", srv.Login)
	mux.HandleFunc("/api/chat", srv.Chat)
	mux.HandleFunc("/api/approve-tool", srv.ApproveTool)
	mux.HandleFunc("/api/apply-changes", srv.ApplyChanges)
	mux.HandleFunc("/api/sessions", srv.Sessions)
	mux.HandleFunc("/api/sessions/", srv.SessionDetail)
	mux.HandleFunc("/api/workspaces", srv.Workspaces)
	mux.HandleFunc("/api/workspaces/", srv.WorkspaceDetail)
	mux.HandleFunc("/api/mode", srv.Mode)

	// UI routes
	mux.HandleFunc("/", srv.Index)
	mux.HandleFunc("/login", srv.LoginPage)
	mux.HandleFunc("/chat", srv.ChatPage)
	mux.Handle("/static/", staticHandler())

	return mux
}

// @title Renia Agentic API
// @version 1.0
// @description Ultra-lightweight Go backend for BitNet RWKV agentic memory inference.
// @host localhost:8080
// @BasePath /
func main() {
	debug.SetGCPercent(20)

	supervisor := NewSupervisor()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	if err := supervisor.EnsureRunning(ctx); err != nil {
		log.Fatalf("supervisor: %v", err)
	}
	cancel()
	defer supervisor.Shutdown()

	db, err := openDB(dbPath)
	if err != nil {
		log.Fatalf("db open: %v", err)
	}
	defer db.Close()

	srv := &Server{db: db, ai: supervisor}
	mux := NewRouter(srv)

	server := &http.Server{
		Addr:         listenAddr,
		Handler:      LoggingAndAuthMiddleware(mux, db),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("renia listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}
