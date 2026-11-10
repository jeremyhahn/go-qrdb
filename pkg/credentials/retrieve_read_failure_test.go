package credentials

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// bufferString copies a retrieved buffer out as a string and wipes it.
func bufferString(buf *secure.Buffer, err error) (string, error) {
	if err != nil {
		return "", err
	}
	defer buf.Destroy()
	out, err := secure.CopyOut(buf)
	return string(out), err
}

// valueFaultStore is a store whose reads of keys under one prefix fail, while
// armed, with the error the replicated backend passes up from a shard without
// a leader. Every other operation reaches the store beneath.
type valueFaultStore struct {
	Store
	prefix string
	armed  atomic.Bool
}

func (v *valueFaultStore) Get(ctx context.Context, key string) ([]byte, error) {
	if v.armed.Load() && strings.HasPrefix(key, v.prefix) {
		return nil, quicraft.ErrShardNotReady
	}
	return v.Store.Get(ctx, key)
}

// A retrieve that could not read the store says so. Told "not found" or
// "expired", a caller would treat a live credential as absent.
func TestRetrieve_StoredValueReadFailureIsNotAbsence(t *testing.T) {
	ctx := context.Background()
	rows := []struct {
		name   string
		prefix string
		op     string
		read   func(*CredentialService) (string, error)
	}{
		{"current value", credentialPrefix, "retrieve", func(s *CredentialService) (string, error) {
			return bufferString(s.RetrieveCredentialBuffer(ctx, "db"))
		}},
		{"current version", credentialPrefix, "retrieve version", func(s *CredentialService) (string, error) {
			return bufferString(s.RetrieveCredentialVersionBuffer(ctx, "db", 2))
		}},
		{"older version in grace", VersionKeyPrefix, "retrieve version", func(s *CredentialService) (string, error) {
			return bufferString(s.RetrieveCredentialVersionBuffer(ctx, "db", 1))
		}},
	}
	want := map[string]string{"current value": "v2", "current version": "v2", "older version in grace": "v1"}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			store := &valueFaultStore{Store: seal.NewMemoryBackend(), prefix: row.prefix}
			svc := newMigratingService(t, seal.NewMemoryBackend())
			require.NoError(t, svc.BindStore(ctx, store))
			require.NoError(t, svc.StoreCredential(ctx, "db", []byte("v1")))
			_, err := svc.RotateCredential(ctx, "db", RotateOptions{NewValue: []byte("v2"), GracePeriod: time.Hour})
			require.NoError(t, err)

			store.armed.Store(true)
			_, err = row.read(svc)
			var opErr *OperationError
			require.ErrorAs(t, err, &opErr)
			assert.Equal(t, row.op, opErr.Op)
			assert.ErrorIs(t, err, quicraft.ErrShardNotReady)
			assert.NotErrorIs(t, err, ErrCredentialNotFound)
			assert.NotErrorIs(t, err, ErrVersionNotFound)
			assert.NotErrorIs(t, err, ErrVersionExpired)

			store.armed.Store(false)
			got, err := row.read(svc)
			require.NoError(t, err)
			assert.Equal(t, want[row.name], got)

			_, err = svc.RetrieveCredentialBuffer(ctx, "never-stored")
			assert.ErrorIs(t, err, ErrCredentialNotFound, "a name never stored is still not found")
		})
	}
}

// A migration claim the node could not read is not a claim nobody holds: the
// migration stops there and is retried, rather than going on as the claimant.
func TestMigrateLegacy_ClaimReadFailureIsNotAbsence(t *testing.T) {
	ctx := context.Background()
	legacy := seal.NewMemoryBackend()
	writeLegacy(t, legacy, "db", "v2", "v1", nil)
	shared := &valueFaultStore{Store: seal.NewMemoryBackend(), prefix: MigrationClaimPrefix}
	shared.armed.Store(true)
	svc := newMigratingService(t, legacy)

	require.NoError(t, svc.BindStore(ctx, shared), "a migration that cannot finish does not fail the bind")
	_, err := shared.Store.Get(ctx, credentialPrefix+"db")
	require.ErrorIs(t, err, seal.ErrSecretNotFound, "nothing is written past a claim that could not be read")
	_, err = svc.RetrieveCredentialBuffer(ctx, "db")
	var opErr *OperationError
	require.ErrorAs(t, err, &opErr)
	assert.Equal(t, "migrate", opErr.Op)
	assert.ErrorIs(t, err, quicraft.ErrShardNotReady)

	shared.armed.Store(false)
	assert.Equal(t, "v2", retrieveString(t, svc, "db"))
}
