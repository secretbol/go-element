// Package matrix wraps mautrix-go: a stored session, an encrypted client and room helpers.
package matrix

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// Session is what login stores in <dir>/session.json (mode 0600).
type Session struct {
	Homeserver  string      `json:"homeserver"`
	UserID      id.UserID   `json:"user_id"`
	DeviceID    id.DeviceID `json:"device_id"`
	AccessToken string      `json:"access_token"`
	// PickleKey encrypts the Olm account in the crypto store. Losing it loses the device's keys.
	PickleKey string `json:"pickle_key"`
}

// DefaultDir is the state directory when none is given.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".go-element"
	}
	return filepath.Join(home, ".local", "state", "go-element")
}

func sessionPath(dir string) string { return filepath.Join(dir, "session.json") }

// LoadSession reads the stored session.
func LoadSession(dir string) (*Session, error) {
	raw, err := os.ReadFile(sessionPath(dir))
	if err != nil {
		return nil, fmt.Errorf("no session in %s (run `go-element login`): %w", dir, err)
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("parsing session: %w", err)
	}
	return &s, nil
}

// Login logs in with a password, creating a new device, and stores the session.
func Login(ctx context.Context, dir, homeserver, user, password, deviceName string) (*Session, error) {
	if _, err := os.Stat(sessionPath(dir)); err == nil {
		return nil, errors.New("a session already exists; remove it (and crypto.db) first, a second login would orphan its keys")
	}
	cli, err := mautrix.NewClient(homeserver, "", "")
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}
	localpart := strings.TrimPrefix(user, "@")
	localpart, _, _ = strings.Cut(localpart, ":")
	resp, err := cli.Login(ctx, &mautrix.ReqLogin{
		Type:                     mautrix.AuthTypePassword,
		Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: localpart},
		Password:                 password,
		InitialDeviceDisplayName: deviceName,
		StoreCredentials:         true,
		StoreHomeserverURL:       true,
	})
	if err != nil {
		return nil, fmt.Errorf("homeserver refused login: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("pickle key: %w", err)
	}
	s := &Session{
		Homeserver:  cli.HomeserverURL.String(),
		UserID:      resp.UserID,
		DeviceID:    resp.DeviceID,
		AccessToken: resp.AccessToken,
		PickleKey:   hex.EncodeToString(key),
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	raw, err := json.MarshalIndent(s, "", "  ") //nolint:gosec // the session file is the point, written 0600
	if err != nil {
		return nil, fmt.Errorf("encoding session: %w", err)
	}
	if err := os.WriteFile(sessionPath(dir), raw, 0o600); err != nil {
		return nil, fmt.Errorf("writing session: %w", err)
	}
	return s, nil
}
