package database_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

var errApplyFault = errors.New("injected apply fault")

// applyFaultStore fails every Put but the commit journal's while armed, so a
// batch commit publishes its journal and then fails to apply it.
type applyFaultStore struct {
	objcommon.Storage
	armed *atomic.Bool
}

func (s applyFaultStore) Put(name string, data io.Reader) error {
	if s.armed.Load() && name != "qrdb-objstore-journal" {
		return errApplyFault
	}
	return s.Storage.Put(name, data)
}

// TestStandaloneBackend_GuardedWriteSettlesObjstoreJournal: a guarded
// compare-and-set on an objstore engine whose last batch failed after
// publishing its journal compares against the batch's value, not the
// pre-batch one the journal is about to replace.
func TestStandaloneBackend_GuardedWriteSettlesObjstoreJournal(t *testing.T) {
	armed := new(atomic.Bool)
	const provider = "test-apply-fault"
	require.NoError(t, objstore.RegisterProvider(provider, objstore.ProviderCredentials{},
		func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
			inner, err := objfactory.NewStorage("memory", map[string]string{}, objcommon.Credentials{})
			if err != nil {
				return nil, err
			}
			return applyFaultStore{Storage: inner, armed: armed}, nil
		}))

	e, err := objstore.New(provider, map[string]string{})
	require.NoError(t, err)
	defer func() { require.NoError(t, e.Close()) }()
	require.NoError(t, e.Set([]byte("k"), []byte("old")))

	armed.Store(true)
	b := e.Batch()
	require.NoError(t, b.Set([]byte("k"), []byte("batch")))
	require.ErrorIs(t, b.Commit(), errApplyFault)
	armed.Store(false)

	backend := database.NewStandaloneBackend(e)
	swapped, err := backend.CompareAndSwap(context.Background(), []byte("k"), []byte("old"), []byte("swapped"))
	require.NoError(t, err)
	assert.False(t, swapped, "the swap compared against the pre-batch value")

	got, err := e.Get([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, "batch", string(got))
}
