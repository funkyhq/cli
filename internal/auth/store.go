package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

// Session is a logged-in CLI: the credential and who it acts as.
type Session struct {
	// Credential is the fkc_ secret. It lives in the OS keychain; the
	// session file carries it only when no keychain is available.
	Credential string    `json:"credential,omitempty"`
	Console    string    `json:"console"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Identity
}

// ErrNotLoggedIn means there is no saved session.
var ErrNotLoggedIn = errors.New("not logged in")

const keyringService = "funky-cli"

// configDir is where the session file lives; FUNKY_CONFIG_DIR overrides it.
func configDir() (string, error) {
	if dir := os.Getenv("FUNKY_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "funky"), nil
}

func sessionPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "session.json"), nil
}

// Save stores the session, its credential in the OS keychain when there is
// one. It reports whether the credential had to go in the file instead.
func Save(s *Session) (inFile bool, err error) {
	path, err := sessionPath()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	onDisk := *s
	if keyring.Set(keyringService, s.Console, s.Credential) == nil {
		onDisk.Credential = ""
	} else {
		inFile = true
	}
	buf, err := json.MarshalIndent(onDisk, "", "  ")
	if err != nil {
		return false, err
	}
	// Write-then-rename so a crash never leaves half a session behind.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return false, err
	}
	return inFile, os.Rename(tmp, path)
}

// Load reads the saved session, or ErrNotLoggedIn.
func Load() (*Session, error) {
	path, err := sessionPath()
	if err != nil {
		return nil, err
	}
	buf, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(buf, &s); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if s.Credential == "" {
		secret, err := keyring.Get(keyringService, s.Console)
		if err != nil {
			return nil, fmt.Errorf("%w (the saved credential is missing from the keychain)", ErrNotLoggedIn)
		}
		s.Credential = secret
	}
	return &s, nil
}

// Delete forgets the saved session, wherever its parts are.
func Delete(s *Session) error {
	if s != nil {
		_ = keyring.Delete(keyringService, s.Console)
	}
	path, err := sessionPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
