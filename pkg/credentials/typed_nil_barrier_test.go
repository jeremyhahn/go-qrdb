package credentials

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// A nil *seal.StorageBarrier wrapped in the Store interface is not a nil
// interface, so an st == nil check lets it through; it must still be
// refused with ErrNilBarrier before anything binds it.
func TestNewCredentialService_TypedNilBarrierIsRefused(t *testing.T) {
	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, (*seal.StorageBarrier)(nil), slog.Default())
	assert.ErrorIs(t, err, ErrNilBarrier)
	assert.Nil(t, svc)
}

func TestBindStore_TypedNilBarrierIsRefused(t *testing.T) {
	svc := newBarrierService(t)
	err := svc.BindStore(context.Background(), (*seal.StorageBarrier)(nil))
	assert.ErrorIs(t, err, ErrNilBarrier)
}

// A nil interface still means "bind later" to the constructor and
// ErrNilStore to BindStore.
func TestNilInterfaceStoreKeepsItsMeaning(t *testing.T) {
	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, nil, slog.Default())
	assert.NoError(t, err)
	assert.ErrorIs(t, svc.BindStore(context.Background(), nil), ErrNilStore)
}
