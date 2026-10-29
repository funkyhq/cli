package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/funkyhq/cli/internal/funky"
)

// Identity is who a CLI session acts as, and where.
type Identity struct {
	User struct {
		Email string  `json:"email"`
		Name  *string `json:"name"`
	} `json:"user"`
	Org     Named `json:"org"`
	Project Named `json:"project"`
}

type Named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// Target is how the CLI names the project it acts on: "Acme / Production".
func (i Identity) Target() string { return i.Org.Name + " / " + i.Project.Name }

// Console is a client for the console's /console/v1/cli/* endpoints.
type Console struct {
	BaseURL string
	HTTP    *http.Client
}

func NewConsole(baseURL string) *Console {
	return &Console{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: http.DefaultClient}
}

type exchangeResponse struct {
	Credential string    `json:"credential"`
	ExpiresAt  time.Time `json:"expires_at"`
	Identity
}

// Exchange trades a one-time login code and the PKCE verifier it was
// issued against for a CLI credential.
func (c *Console) Exchange(ctx context.Context, code, verifier string) (*exchangeResponse, error) {
	var out exchangeResponse
	err := c.post(ctx, "/console/v1/cli/token", "", map[string]string{"code": code, "code_verifier": verifier}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

type accessResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Identity
}

// AccessToken trades a CLI credential for a short-lived access token for
// the session's project, rechecking the user's access on the way.
func (c *Console) AccessToken(ctx context.Context, credential string) (*accessResponse, error) {
	var out accessResponse
	if err := c.post(ctx, "/console/v1/cli/access-token", credential, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Logout revokes a CLI credential.
func (c *Console) Logout(ctx context.Context, credential string) error {
	return c.post(ctx, "/console/v1/cli/logout", credential, nil, nil)
}

func (c *Console) post(ctx context.Context, path, bearer string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
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
		return funky.ParseError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("funky console: decoding %s: %w", path, err)
	}
	return nil
}

// TokenSource hands out the session's access token, minting a fresh one
// from the CLI credential shortly before the current one expires — so a
// long apply never runs on an expired token.
type TokenSource struct {
	Console    *Console
	Credential string
	Now        func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// refreshMargin is how long before expiry a token is replaced.
const refreshMargin = 30 * time.Second

func (s *TokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if s.token != "" && now().Add(refreshMargin).Before(s.expires) {
		return s.token, nil
	}
	resp, err := s.Console.AccessToken(ctx, s.Credential)
	if err != nil {
		return "", err
	}
	s.token = resp.AccessToken
	s.expires = now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	return s.token, nil
}
