package seal_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
)

// TestStorageBarrier_VerifyCredentials_DeviceStrategy drives VerifyCredentials
// through the sealtest device strategy: a refused credential is a mismatch,
// and a device fault is an unseal failure, never a mismatch.
func TestStorageBarrier_VerifyCredentials_DeviceStrategy(t *testing.T) {
	const pass = "verify-device-pass"
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	newBarrier := func(t *testing.T) (*seal.StorageBarrier, *sealtest.DeviceStrategy) {
		t.Helper()
		dev, err := sealtest.NewDeviceStrategy(seal.StrategyID("sealtest-device"))
		if err != nil {
			t.Fatalf("NewDeviceStrategy: %v", err)
		}
		b, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(),
			seal.BarrierConfig{RootKeyPath: "verify/device-root-key"}, dev)
		if err != nil {
			t.Fatalf("NewStorageBarrier: %v", err)
		}
		if err := b.Initialize(ctx, seal.MustCredentials(ctx, []byte(pass))); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		return b, dev
	}
	verify := func(t *testing.T, b *seal.StorageBarrier, secret string) error {
		t.Helper()
		creds := seal.MustCredentials(ctx, []byte(secret))
		defer seal.DestroyCredentials(creds)
		return b.VerifyCredentials(ctx, creds)
	}

	t.Run("right credential verifies", func(t *testing.T) {
		b, _ := newBarrier(t)
		if err := verify(t, b, pass); err != nil {
			t.Fatalf("VerifyCredentials = %v, want nil", err)
		}
	})

	t.Run("wrong credential is a mismatch", func(t *testing.T) {
		b, _ := newBarrier(t)
		err := verify(t, b, "not-the-pass")
		if !errors.Is(err, seal.ErrCredentialMismatch) || errors.Is(err, seal.ErrUnsealFailed) {
			t.Fatalf("VerifyCredentials = %v, want ErrCredentialMismatch and not ErrUnsealFailed", err)
		}
	})

	t.Run("device fault is an unseal failure", func(t *testing.T) {
		b, dev := newBarrier(t)
		errDevice := errors.New("sealtest device: token removed")
		dev.InjectFault(errDevice)
		err := verify(t, b, pass)
		var ue *seal.UnsealError
		if !errors.As(err, &ue) {
			t.Fatalf("VerifyCredentials = %T (%v), want *seal.UnsealError", err, err)
		}
		if !errors.Is(err, seal.ErrUnsealFailed) {
			t.Fatalf("VerifyCredentials = %v, want ErrUnsealFailed", err)
		}
		if errors.Is(err, seal.ErrCredentialMismatch) {
			t.Fatalf("VerifyCredentials = %v, must not be ErrCredentialMismatch", err)
		}
		if !errors.Is(err, errDevice) {
			t.Fatalf("VerifyCredentials = %v, want it to wrap the device fault", err)
		}
	})
}
