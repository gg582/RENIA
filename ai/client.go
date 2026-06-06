package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"renia/config"
)

// Client is a thin HTTP wrapper around the local RWKV inference cluster.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient builds a client targeting the configured AIEndpoint.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: config.AITimeout,
		},
		baseURL: config.AIEndpoint,
	}
}

// Chat sends a complete message history to the inference engine and returns
// the assistant's reply text.
func (c *Client) Chat(ctx context.Context, messages []ChatMessage) (string, error) {
	reqBody := ChatRequest{
		Model:    "bitnet-rwkv",
		Messages: messages,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(reqBody); err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, &buf)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("inference request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("inference status %d: %s", resp.StatusCode, string(body))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("empty inference choice set")
	}
	return chatResp.Choices[0].Message.Content, nil
}
