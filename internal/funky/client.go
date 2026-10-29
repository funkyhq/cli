// Package funky is a minimal client for the Funky API's /v1 surface.
package funky

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// AgentConfig is an agent config as the API returns it. The spec fields
// are kept as raw JSON so a plan can compare them with a local file
// without this package modelling every nested shape.
type AgentConfig struct {
	ID            string          `json:"id"`
	Version       int             `json:"version"`
	ArchivedAt    string          `json:"archivedAt,omitempty"`
	Inference     json.RawMessage `json:"inference"`
	SystemPrompt  json.RawMessage `json:"systemPrompt"`
	Network       json.RawMessage `json:"network"`
	Packages      json.RawMessage `json:"packages"`
	Credentials   json.RawMessage `json:"credentials"`
	SetupCommands json.RawMessage `json:"setupCommands"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

// APIError is the API's error envelope.
type APIError struct {
	Status    int
	Type      string
	Message   string
	RequestID string
}

func (e *APIError) Error() string {
	if e.Type == "" {
		return fmt.Sprintf("funky: HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("funky: %s (HTTP %d): %s", e.Type, e.Status, e.Message)
}

// TokenSource supplies the bearer token for each request.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a token that never changes, like an API key.
type StaticToken string

func (t StaticToken) Token(context.Context) (string, error) { return string(t), nil }

type Client struct {
	BaseURL string
	Auth    TokenSource
	HTTP    *http.Client
}

func NewClient(baseURL string, auth TokenSource) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Auth: auth, HTTP: http.DefaultClient}
}

// ListAgentConfigs reads every page of the caller's agent configs,
// archived ones included.
func (c *Client) ListAgentConfigs(ctx context.Context) ([]AgentConfig, error) {
	var all []AgentConfig
	after := ""
	for {
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after", after)
		}
		var page struct {
			Data    []AgentConfig `json:"data"`
			HasMore bool          `json:"hasMore"`
			LastID  string        `json:"lastId"`
		}
		if err := c.do(ctx, http.MethodGet, "/v1/agent-configs?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if !page.HasMore || page.LastID == "" {
			return all, nil
		}
		after = page.LastID
	}
}

func (c *Client) CreateAgentConfig(ctx context.Context, body any) (*AgentConfig, error) {
	var out AgentConfig
	if err := c.do(ctx, http.MethodPost, "/v1/agent-configs", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateAgentConfig replaces the fields present in body. body should carry
// the version it was planned against, so a concurrent edit answers 409.
func (c *Client) UpdateAgentConfig(ctx context.Context, id string, body any) (*AgentConfig, error) {
	var out AgentConfig
	if err := c.do(ctx, http.MethodPost, "/v1/agent-configs/"+url.PathEscape(id), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	token, err := c.Auth.Token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return ParseError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("funky api: decoding %s %s: %w", method, path, err)
	}
	return nil
}

// ParseError reads the error envelope Funky's services share.
func ParseError(status int, raw []byte) error {
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error.Message != "" {
		return &APIError{Status: status, Type: env.Error.Type, Message: env.Error.Message, RequestID: env.RequestID}
	}
	msg := strings.TrimSpace(string(raw))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &APIError{Status: status, Message: msg}
}
