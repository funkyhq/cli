package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// fakeCloud plays the platform page and the console: authorize() is what
// clicking "Authorize" does, and the console half checks PKCE like the
// real /console/v1/cli/token.
type fakeCloud struct {
	mu         sync.Mutex
	codes      map[string]string // code -> challenge
	accessHits int
	srv        *httptest.Server
}

func newFakeCloud(t *testing.T) *fakeCloud {
	f := &fakeCloud{codes: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/console/v1/cli/token":
			var body struct{ Code, Code_verifier string }
			json.NewDecoder(r.Body).Decode(&body)
			challenge, ok := f.codes[body.Code]
			delete(f.codes, body.Code)
			sum := sha256.Sum256([]byte(body.Code_verifier))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid or expired authorization code"}}`)
				return
			}
			fmt.Fprint(w, `{"credential":"fkc_test","expires_at":"2027-01-01T00:00:00Z",
				"user":{"email":"alice@example.com","name":"Alice"},
				"org":{"id":"o1","name":"Acme","slug":"acme"},"project":{"id":"p1","name":"Production","slug":"production"}}`)
		case "/console/v1/cli/access-token":
			f.accessHits++
			if r.Header.Get("Authorization") != "Bearer fkc_test" {
				w.WriteHeader(401)
				fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid CLI credential"}}`)
				return
			}
			fmt.Fprintf(w, `{"access_token":"managed_%d","expires_in":300,"org":{"name":"Acme"},"project":{"name":"Production"}}`, f.accessHits)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCloud) endpoints() Endpoints {
	return Endpoints{API: f.srv.URL, Console: f.srv.URL, Platform: "https://platform.test"}
}

// authorize is the platform page after the user clicks "Authorize".
func (f *fakeCloud) authorize(link string) (code string, q url.Values) {
	u, _ := url.Parse(link)
	q = u.Query()
	code = fmt.Sprintf("code-%d", len(f.codes)+1)
	f.mu.Lock()
	f.codes[code] = q.Get("code_challenge")
	f.mu.Unlock()
	return code, q
}

// visit follows the platform's redirect to the CLI's listener, like the
// browser, and returns where the CLI sends the tab next.
func visit(t *testing.T, port string, params url.Values) string {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get("http://127.0.0.1:" + port + "/callback?" + params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("Location")
}

func TestChallengeIsS256(t *testing.T) {
	// RFC 7636 appendix B.
	if got := challengeFor("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge = %s", got)
	}
}

func TestLoopbackLogin(t *testing.T) {
	cloud := newFakeCloud(t)
	browser := make(chan []string, 1)
	var saved *Session
	var out bytes.Buffer
	session, err := Login(context.Background(), LoginOptions{
		Endpoints:  cloud.endpoints(),
		DeviceName: "alice-laptop",
		Out:        &out,
		Save:       func(s *Session) error { saved = s; return nil },
		OpenBrowser: func(link string) error {
			if !strings.HasPrefix(link, "https://platform.test/cli/authorize?") {
				t.Errorf("opened %s", link)
			}
			code, q := cloud.authorize(link)
			if q.Get("device") != "alice-laptop" || q.Get("code_challenge_method") != "S256" {
				t.Errorf("authorize query = %v", q)
			}
			go func() {
				// A stray request with the wrong state is refused, and login keeps waiting.
				forged := visit(t, q.Get("port"), url.Values{"code": {code}, "state": {"forged"}})
				browser <- []string{forged, visit(t, q.Get("port"), url.Values{"code": {code}, "state": {q.Get("state")}})}
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Credential != "fkc_test" || session.Target() != "Acme / Production" || saved != session {
		t.Fatalf("session = %+v", session)
	}
	landed := <-browser
	want := []string{"https://platform.test/cli/error?reason=state", "https://platform.test/cli/success"}
	if strings.Join(landed, " ") != strings.Join(want, " ") {
		t.Fatalf("browser landed on %v; want %v", landed, want)
	}
	if !strings.Contains(out.String(), "Opening your browser") {
		t.Errorf("output = %q", out.String())
	}
}

func TestLoopbackLoginCancelledInBrowser(t *testing.T) {
	cloud := newFakeCloud(t)
	browser := make(chan string, 1)
	_, err := Login(context.Background(), LoginOptions{
		Endpoints: cloud.endpoints(),
		Out:       &bytes.Buffer{},
		OpenBrowser: func(link string) error {
			_, q := cloud.authorize(link)
			go func() {
				browser <- visit(t, q.Get("port"), url.Values{"error": {"access_denied"}, "state": {q.Get("state")}})
			}()
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}
	if landed := <-browser; landed != "https://platform.test/cli/error?reason=denied" {
		t.Fatalf("landed = %s", landed)
	}
}

func TestLoopbackLoginRejectedCodeShowsExpired(t *testing.T) {
	cloud := newFakeCloud(t)
	browser := make(chan string, 1)
	var saved bool
	_, err := Login(context.Background(), LoginOptions{
		Endpoints: cloud.endpoints(),
		Out:       &bytes.Buffer{},
		Save:      func(*Session) error { saved = true; return nil },
		OpenBrowser: func(link string) error {
			_, q := cloud.authorize(link)
			go func() {
				browser <- visit(t, q.Get("port"), url.Values{"code": {"never-issued"}, "state": {q.Get("state")}})
			}()
			return nil
		},
	})
	if err == nil || saved {
		t.Fatalf("err = %v, saved = %v; want an error and nothing saved", err, saved)
	}
	if landed := <-browser; landed != "https://platform.test/cli/error?reason=expired" {
		t.Fatalf("landed = %s", landed)
	}
}

func TestPasteLogin(t *testing.T) {
	cloud := newFakeCloud(t)
	out := &syncWriter{}
	// The user reads the link, approves it, and pastes the page's code.
	reader, writer := io.Pipe()
	go func() {
		for !strings.Contains(out.String(), "paste the code") {
			time.Sleep(5 * time.Millisecond)
		}
		link := strings.Fields(strings.SplitN(out.String(), "\n\n", 3)[1])[0]
		code, q := cloud.authorize(link)
		if q.Has("port") {
			t.Errorf("--no-browser link has a port: %s", link)
		}
		fmt.Fprintln(writer, code)
	}()
	session, err := Login(context.Background(), LoginOptions{
		Endpoints: cloud.endpoints(),
		NoBrowser: true,
		In:        reader,
		Out:       out,
	})
	if err != nil || session.Credential != "fkc_test" {
		t.Fatalf("session = %+v, err = %v", session, err)
	}
}

func TestTokenSourceRefreshesBeforeExpiry(t *testing.T) {
	cloud := newFakeCloud(t)
	now := time.Unix(0, 0)
	src := &TokenSource{Console: NewConsole(cloud.srv.URL), Credential: "fkc_test", Now: func() time.Time { return now }}
	first, _ := src.Token(context.Background())
	again, _ := src.Token(context.Background())
	if first != "managed_1" || again != first {
		t.Fatalf("tokens = %s, %s; want one cached token", first, again)
	}
	now = now.Add(5*time.Minute - 10*time.Second) // inside the refresh margin
	if next, _ := src.Token(context.Background()); next != "managed_2" {
		t.Fatalf("token = %s; want a fresh one", next)
	}
	bad := &TokenSource{Console: NewConsole(cloud.srv.URL), Credential: "fkc_revoked"}
	if _, err := bad.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid CLI credential") {
		t.Fatalf("err = %v", err)
	}
}

func TestStoreKeepsTheCredentialInTheKeychain(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	t.Setenv("FUNKY_CONFIG_DIR", dir)
	if _, err := Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Load before login = %v", err)
	}
	s := &Session{Credential: "fkc_secret", Console: "https://api.funky.dev"}
	s.User.Email = "alice@example.com"
	inFile, err := Save(s)
	if err != nil || inFile {
		t.Fatalf("Save = %v, %v", inFile, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	if strings.Contains(string(raw), "fkc_secret") {
		t.Fatal("credential written to the session file despite a keychain")
	}
	got, err := Load()
	if err != nil || got.Credential != "fkc_secret" || got.User.Email != "alice@example.com" {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if err := Delete(got); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Load after Delete = %v", err)
	}
}

func TestStoreFallsBackToAPrivateFile(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keychain"))
	dir := t.TempDir()
	t.Setenv("FUNKY_CONFIG_DIR", dir)
	inFile, err := Save(&Session{Credential: "fkc_secret", Console: "c"})
	if err != nil || !inFile {
		t.Fatalf("Save = %v, %v; want the file fallback", inFile, err)
	}
	info, _ := os.Stat(filepath.Join(dir, "session.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode = %v", info.Mode().Perm())
	}
	got, err := Load()
	if err != nil || got.Credential != "fkc_secret" {
		t.Fatalf("Load = %+v, %v", got, err)
	}
}

// syncWriter is a buffer the test can read while Login writes to it.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestStoreKeepsConfigDirsApart(t *testing.T) {
	keyring.MockInit()
	main, scratch := t.TempDir(), t.TempDir()
	t.Setenv("FUNKY_CONFIG_DIR", main)
	Save(&Session{Credential: "fkc_main", Console: "https://api.funky.dev"})
	t.Setenv("FUNKY_CONFIG_DIR", scratch)
	scratchSession := &Session{Credential: "fkc_scratch", Console: "https://api.funky.dev"}
	Save(scratchSession)
	Delete(scratchSession)

	t.Setenv("FUNKY_CONFIG_DIR", main)
	got, err := Load()
	if err != nil || got.Credential != "fkc_main" {
		t.Fatalf("main login after a scratch login and logout = %+v, %v; want fkc_main untouched", got, err)
	}
}

func TestLoadReportsAnUnreadableKeychain(t *testing.T) {
	keyring.MockInit()
	t.Setenv("FUNKY_CONFIG_DIR", t.TempDir())
	Save(&Session{Credential: "fkc_secret", Console: "c"})

	keyring.MockInitWithError(errors.New("keychain is locked"))
	_, err := Load()
	if err == nil || errors.Is(err, ErrNotLoggedIn) || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("Load = %v; want the keychain error, not ErrNotLoggedIn", err)
	}

	keyring.MockInit() // an empty keychain: the entry is really gone
	if _, err := Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("Load with the entry missing = %v; want ErrNotLoggedIn", err)
	}
}
