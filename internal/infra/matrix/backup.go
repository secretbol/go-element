package matrix

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/event"
)

const backupKeyLen = 32

// ErrBadSecret means the backup secret in secret storage has a format this tool does not know.
var ErrBadSecret = errors.New("unrecognised backup key secret")

// ErrNoBackup means the account has no server-side key backup to restore from.
var ErrNoBackup = errors.New("the account has no key backup")

// RestoreFromBackup unlocks secret storage with the recovery key, takes the
// key-backup secret out of it and imports every room key from the latest
// server-side backup, so this device can read history from before it existed.
// It returns the backup version it restored.
func (c *Client) RestoreFromBackup(ctx context.Context, recoveryKey string) (string, error) {
	ss := ssss.NewSSSSMachine(c.cli)
	keyID, keyData, err := ss.GetDefaultKeyData(ctx)
	if err != nil {
		return "", fmt.Errorf("secret storage not set up: %w", err)
	}
	key, err := keyData.VerifyRecoveryKey(keyID, strings.TrimSpace(recoveryKey))
	if err != nil {
		return "", fmt.Errorf("recovery key rejected: %w", err)
	}
	secret, err := ss.GetDecryptedAccountData(ctx, event.AccountDataMegolmBackupKey, key)
	if err != nil {
		return "", fmt.Errorf("reading backup key from secret storage: %w", err)
	}
	raw, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	backupKey, err := backup.MegolmBackupKeyFromBytes(raw)
	if err != nil {
		return "", fmt.Errorf("backup key: %w", err)
	}

	ctx = zerolog.New(os.Stderr).Level(zerolog.InfoLevel).With().Timestamp().Logger().WithContext(ctx)
	version, err := c.crypto.Machine().DownloadAndStoreLatestKeyBackup(ctx, backupKey)
	if err != nil {
		return "", fmt.Errorf("restoring key backup: %w", err)
	}
	if version == "" {
		return "", ErrNoBackup
	}
	return string(version), nil
}

// decodeSecret turns the decrypted backup secret into the 32-byte private key.
// Clients store it as base64 text, sometimes JSON-quoted, and some store the raw bytes.
func decodeSecret(secret []byte) ([]byte, error) {
	text := strings.TrimSpace(string(secret))
	var quoted string
	if json.Unmarshal([]byte(text), &quoted) == nil {
		text = strings.TrimSpace(quoted)
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(text); err == nil && len(raw) == backupKeyLen {
			return raw, nil
		}
	}
	if len(secret) == backupKeyLen {
		return secret, nil
	}
	return nil, fmt.Errorf("%w: %d bytes, starts with %s", ErrBadSecret, len(secret), kind(secret))
}

func kind(b []byte) string {
	switch {
	case len(b) == 0:
		return "nothing"
	case b[0] == '"' || b[0] == '{' || b[0] == '[':
		return "json punctuation"
	case b[0] >= 0x20 && b[0] < 0x7f:
		return "printable text"
	default:
		return "binary"
	}
}
