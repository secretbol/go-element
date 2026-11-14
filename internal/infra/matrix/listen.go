package matrix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// SASPrompt shows the short authentication string and reports whether the user
// saw the same emojis on the other device.
type SASPrompt func(ctx context.Context, emojis []rune, descriptions []string, decimals []int) (bool, error)

// Listener answers verification requests from the owner's other devices.
type Listener struct {
	c      *Client
	vh     *verificationhelper.VerificationHelper
	prompt SASPrompt
	done   chan error
}

// Listen registers the verification handlers. Call it from the setup hook of
// OpenWith, then Wait for the outcome of the first verification.
func Listen(c *Client, prompt SASPrompt) (*Listener, error) {
	l := &Listener{c: c, prompt: prompt, done: make(chan error, 1)}
	l.vh = verificationhelper.NewVerificationHelper(c.cli, c.crypto.Machine(), nil, l, false, false, true)
	if err := l.vh.Init(context.Background()); err != nil {
		return nil, fmt.Errorf("verification helper: %w", err)
	}
	return l, nil
}

// Wait blocks until one verification finishes or is cancelled.
func (l *Listener) Wait(ctx context.Context) error {
	select {
	case err := <-l.done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("waiting for verification: %w", ctx.Err())
	}
}

func (l *Listener) finish(err error) {
	select {
	case l.done <- err:
	default:
	}
}

// VerificationRequested accepts requests from our own user only.
func (l *Listener) VerificationRequested(ctx context.Context, txn id.VerificationTransactionID, from id.UserID, fromDevice id.DeviceID) {
	if from != l.c.UserID() {
		slog.Warn("ignoring verification request from another user", "from", from)
		_ = l.vh.DismissVerification(ctx, txn)
		return
	}
	slog.Info("verification requested", "device", fromDevice)
	if err := l.vh.AcceptVerification(ctx, txn); err != nil {
		l.finish(fmt.Errorf("accepting request: %w", err))
	}
}

// VerificationReady starts emoji verification once both sides agreed to verify.
func (l *Listener) VerificationReady(ctx context.Context, txn id.VerificationTransactionID, other id.DeviceID, sas, _ bool, _ *verificationhelper.QRCode) {
	if !sas {
		_ = l.vh.CancelVerification(ctx, txn, event.VerificationCancelCodeUnknownMethod, "emoji verification not supported by the other device")
		l.finish(errors.New("the other device does not support emoji verification"))
		return
	}
	slog.Info("ready, starting emoji verification", "device", other)
	if err := l.vh.StartSAS(ctx, txn); err != nil {
		l.finish(fmt.Errorf("starting SAS: %w", err))
	}
}

// ShowSAS asks the user to compare the emojis, then confirms or cancels.
func (l *Listener) ShowSAS(ctx context.Context, txn id.VerificationTransactionID, emojis []rune, descriptions []string, decimals []int) {
	ok, err := l.prompt(ctx, emojis, descriptions, decimals)
	if err != nil || !ok {
		_ = l.vh.CancelVerification(ctx, txn, event.VerificationCancelCodeSASMismatch, "emojis rejected")
		if err == nil {
			err = errors.New("emojis rejected")
		}
		l.finish(err)
		return
	}
	if err := l.vh.ConfirmSAS(ctx, txn); err != nil {
		l.finish(fmt.Errorf("confirming SAS: %w", err))
	}
}

// VerificationCancelled ends the wait with the cancel reason.
func (l *Listener) VerificationCancelled(_ context.Context, _ id.VerificationTransactionID, code event.VerificationCancelCode, reason string) {
	l.finish(fmt.Errorf("verification cancelled (%s): %s", code, reason))
}

// VerificationDone ends the wait successfully.
func (l *Listener) VerificationDone(_ context.Context, _ id.VerificationTransactionID, method event.VerificationMethod) {
	slog.Info("verification done", "method", method)
	l.finish(nil)
}
