package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/funkyhq/cli/internal/funky"
)

// LoginTimeout bounds how long login waits for the browser.
const LoginTimeout = 10 * time.Minute

type LoginOptions struct {
	Endpoints  Endpoints
	DeviceName string
	// NoBrowser prints the URL and reads the code the page shows from In,
	// for machines without a browser (SSH, containers).
	NoBrowser bool
	// OpenBrowser opens a URL; defaults to the OS's opener.
	OpenBrowser func(string) error
	// Save persists the new session. It runs before the browser is told
	// the CLI is authenticated, so the page never says so prematurely.
	Save func(*Session) error
	In   io.Reader
	Out  io.Writer
}

// Login runs the browser login: it asks the platform to authorize this
// CLI, receives a one-time code on a loopback listener (or from the user,
// with NoBrowser), and exchanges it — with a PKCE verifier that never
// leaves this process — for a CLI credential.
func Login(ctx context.Context, o LoginOptions) (*Session, error) {
	verifier := randomString(32)
	q := url.Values{
		"state":                 {randomString(24)},
		"code_challenge":        {challengeFor(verifier)},
		"code_challenge_method": {"S256"},
		"device":                {o.DeviceName},
	}
	console := NewConsole(o.Endpoints.Console)
	finish := func(ctx context.Context, code string) (*Session, error) {
		resp, err := console.Exchange(ctx, code, verifier)
		if err != nil {
			return nil, err
		}
		s := &Session{
			Credential: resp.Credential,
			Console:    o.Endpoints.Console,
			ExpiresAt:  resp.ExpiresAt,
			Identity:   resp.Identity,
		}
		if o.Save != nil {
			if err := o.Save(s); err != nil {
				return nil, fmt.Errorf("saving the login: %w", err)
			}
		}
		return s, nil
	}

	ctx, cancel := context.WithTimeout(ctx, LoginTimeout)
	defer cancel()
	if o.NoBrowser {
		return pasteLogin(ctx, o, authorizeURL(o.Endpoints, q), finish)
	}
	return loopbackLogin(ctx, o, q, finish)
}

type finisher func(context.Context, string) (*Session, error)

func pasteLogin(ctx context.Context, o LoginOptions, link string, finish finisher) (*Session, error) {
	fmt.Fprintf(o.Out, "Open this link in a browser to log in:\n\n  %s\n\nThen paste the code it shows: ", link)
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(o.In).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	select {
	case <-ctx.Done():
		return nil, loginTimeoutErr(ctx)
	case code := <-lines:
		if code == "" {
			return nil, errors.New("no code entered; login cancelled")
		}
		return finish(ctx, code)
	}
}

func loopbackLogin(ctx context.Context, o LoginOptions, q url.Values, finish finisher) (*Session, error) {
	// 127.0.0.1, not localhost: the platform only ever redirects there, and
	// binding the literal address keeps the listener off other interfaces.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting the login listener: %w", err)
	}
	q.Set("port", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	state := q.Get("state")

	type result struct {
		session *Session
		err     error
	}
	results := make(chan result, 1)
	var once sync.Once
	page := func(w http.ResponseWriter, r *http.Request, path string) {
		http.Redirect(w, r, o.Endpoints.Platform+path, http.StatusFound)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		cb := r.URL.Query()
		// Not from the page this login opened: refuse it, keep waiting.
		if cb.Get("state") != state {
			page(w, r, "/cli/error?reason=state")
			return
		}
		handled := false
		once.Do(func() {
			handled = true
			if cb.Get("error") != "" {
				page(w, r, "/cli/error?reason=denied")
				results <- result{err: errors.New("login cancelled in the browser")}
				return
			}
			s, err := finish(r.Context(), cb.Get("code"))
			if err != nil {
				reason := "failed"
				var apiErr *funky.APIError
				if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
					reason = "expired"
				}
				page(w, r, "/cli/error?reason="+reason)
				results <- result{err: err}
				return
			}
			page(w, r, "/cli/success")
			results <- result{session: s}
		})
		if !handled {
			page(w, r, "/cli/error?reason=expired")
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer func() {
		// Let the redirect that ended the login reach the browser.
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	link := authorizeURL(o.Endpoints, q)
	open := o.OpenBrowser
	if open == nil {
		open = openBrowser
	}
	if err := open(link); err != nil {
		fmt.Fprintf(o.Out, "Open this link in a browser to log in:\n\n  %s\n\n", link)
	} else {
		fmt.Fprintf(o.Out, "Opening your browser to log in. If it didn't open, visit:\n\n  %s\n\n", link)
	}
	fmt.Fprintln(o.Out, "Waiting for you to finish in the browser…")

	select {
	case <-ctx.Done():
		return nil, loginTimeoutErr(ctx)
	case res := <-results:
		return res.session, res.err
	}
}

func authorizeURL(e Endpoints, q url.Values) string {
	return e.Platform + "/cli/authorize?" + q.Encode()
}

func loginTimeoutErr(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("timed out waiting for the browser; run `funky login` again")
	}
	return ctx.Err()
}

// randomString is n random bytes, base64url without padding.
func randomString(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// challengeFor is PKCE S256: base64url(sha256(verifier)).
func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func openBrowser(link string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	return cmd.Start()
}
