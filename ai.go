package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	defaultBaseURL      = "http://127.0.0.1:9090"
	healthEndpoint      = "/health"
	chatEndpoint        = "/v1/chat/completions"
	startupTimeout      = 30 * time.Second
	healthCheckInterval = 500 * time.Millisecond
)

// ChatMessage models a single turn in the chat payload.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the JSON payload sent to the inference endpoint.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

// ChatResponse mirrors the expected JSON shape from the RWKV server.
type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// Supervisor manages the C++ inference host lifecycle and provides the HTTP client.
type Supervisor struct {
	client     *http.Client
	baseURL    string
	cppPath    string
	cppProcess *os.Process
}

// NewSupervisor creates a supervisor with default settings.
func NewSupervisor() *Supervisor {
	return &Supervisor{
		client:  &http.Client{Timeout: 120 * time.Second},
		baseURL: defaultBaseURL,
		cppPath: findCppBinary(),
	}
}

func findCppBinary() string {
	ex, err := os.Executable()
	if err != nil {
		ex = "."
	}
	dir := filepath.Dir(ex)
	candidates := []string{
		filepath.Join(dir, "build", "cpp", "rwkv_server"),
		filepath.Join(dir, "cpp", "build", "rwkv_server"),
		filepath.Join(dir, "build", "rwkv_server"),
		"./build/cpp/rwkv_server",
		"./cpp/build/rwkv_server",
		"../build/cpp/rwkv_server",
		"rwkv_server",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// EnsureRunning checks C++ host health and auto-starts it if missing.
func (s *Supervisor) EnsureRunning(ctx context.Context) error {
	if s.isHealthy(ctx) {
		return nil
	}
	if s.cppPath == "" {
		return fmt.Errorf("rwkv_server binary not found in any known location")
	}
	if err := s.startCppHost(); err != nil {
		return err
	}
	return s.waitForHealth(ctx)
}

func (s *Supervisor) isHealthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+healthEndpoint, nil)
	if err != nil {
		return false
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func findLibDir(binPath string) string {
	dir := filepath.Dir(binPath)
	candidates := []string{
		filepath.Join(dir, "..", "..", "third_party", "rwkv.cpp"),
		filepath.Join(dir, "..", "third_party", "rwkv.cpp"),
		filepath.Join(dir, "third_party", "rwkv.cpp"),
		"./build/third_party/rwkv.cpp",
		"../build/third_party/rwkv.cpp",
	}
	for _, c := range candidates {
		if info, err := os.Stat(filepath.Join(c, "librwkv.so")); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

func (s *Supervisor) startCppHost() error {
	cmd := exec.Command(s.cppPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if libDir := findLibDir(s.cppPath); libDir != "" {
		ldPath := os.Getenv("LD_LIBRARY_PATH")
		if ldPath != "" {
			ldPath = libDir + ":" + ldPath
		} else {
			ldPath = libDir
		}
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+ldPath)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("fork rwkv_server: %w", err)
	}
	s.cppProcess = cmd.Process
	return nil
}

func (s *Supervisor) waitForHealth(parentCtx context.Context) error {
	ctx, cancel := context.WithTimeout(parentCtx, startupTimeout)
	defer cancel()

	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if s.isHealthy(ctx) {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("rwkv_server health check timed out after %v", startupTimeout)
		}
	}
}

// Chat forwards messages to the C++ inference engine and returns the assistant content.
func (s *Supervisor) Chat(ctx context.Context, messages []chatMessage) (string, error) {
	reqBody := chatRequest{
		Model:    "bitnet-rwkv",
		Messages: messages,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(reqBody); err != nil {
		return "", fmt.Errorf("encode chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+chatEndpoint, &buf)
	if err != nil {
		return "", fmt.Errorf("build chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("inference request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("inference status %d: %s", resp.StatusCode, string(body))
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("empty choice set from inference engine")
	}
	return chatResp.Choices[0].Message.Content, nil
}

// Shutdown terminates the supervised C++ child process if it was started by this supervisor.
func (s *Supervisor) Shutdown() error {
	if s.cppProcess != nil {
		return s.cppProcess.Kill()
	}
	return nil
}
