package matrix

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog"
)

// VerifyStatus says whether the account has cross-signing keys and whether this device is signed by them.
type VerifyStatus struct {
	HasKeys  bool
	Verified bool
}

// VerificationStatus reports if this device is cross-signed by its owner.
func (c *Client) VerificationStatus(ctx context.Context) (VerifyStatus, error) {
	has, ok, err := c.crypto.Machine().GetOwnVerificationStatus(ctx)
	if err != nil {
		return VerifyStatus{}, fmt.Errorf("verification status: %w", err)
	}
	return VerifyStatus{HasKeys: has, Verified: ok}, nil
}

// VerifyWithRecoveryKey unlocks secret storage with the recovery key, imports the
// cross-signing keys and signs this device and the master key, so other clients
// show this session as verified.
func (c *Client) VerifyWithRecoveryKey(ctx context.Context, recoveryKey string) error {
	ctx = zerolog.New(os.Stderr).Level(zerolog.InfoLevel).With().Timestamp().Logger().WithContext(ctx)
	if err := c.crypto.Machine().VerifyWithRecoveryKey(ctx, strings.TrimSpace(recoveryKey)); err != nil {
		return fmt.Errorf("verify with recovery key: %w", err)
	}
	return nil
}
