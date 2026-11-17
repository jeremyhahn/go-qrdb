package ceremony

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

var errWriteAfterMarker = errors.New("simulated storage fault after the SO write-ahead marker")

// failAfterMarkerBackend fails the first write that follows the system SO
// write-ahead marker, so the Shamir initialization the marker was written
// for fails and rolls back.
type failAfterMarkerBackend struct {
	seal.StorageBackend
	armed bool
	fired bool
}

func (b *failAfterMarkerBackend) Put(ctx context.Context, key string, value []byte) error {
	if strings.HasSuffix(key, systemSOWriteOutstandingKey) {
		b.armed = !b.fired
		return b.StorageBackend.Put(ctx, key, value)
	}
	if b.armed {
		b.armed = false
		b.fired = true
		return errWriteAfterMarker
	}
	return b.StorageBackend.Put(ctx, key, value)
}

// markerKeys lists the backend keys holding the system SO write-ahead marker.
func (b *failAfterMarkerBackend) markerKeys(t *testing.T) []string {
	t.Helper()
	keys, err := b.StorageBackend.List(context.Background(), "")
	require.NoError(t, err)
	var out []string
	for _, k := range keys {
		if strings.HasSuffix(k, systemSOWriteOutstandingKey) {
			out = append(out, k)
		}
	}
	return out
}

// TestInitialize_ShamirRollbackRemovesSystemSOMarker: a Shamir initialization
// that fails after Distribute wrote the system SO write-ahead marker rolls
// back without leaving the marker, so after a restart a fresh initialization
// without an SO completes once its holders claim, instead of staying
// enrolling on a marker the new barrier cannot read.
func TestInitialize_ShamirRollbackRemovesSystemSOMarker(t *testing.T) {
	ctx := context.Background()
	backend := &failAfterMarkerBackend{StorageBackend: seal.NewMemoryBackend()}
	e := &holderEnv{t: t, backend: backend, ca: testCA(t), pins: pintest.NewBackend(t), factors: testFactors(t)}
	e.barrier = shamirBarrier(t, e.backend, 2, 3)
	e.svc = e.newService(e.barrier)

	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	carol := newHolder(t, "carol", seal.RoleSecurityOfficer)
	soCSR, _ := generateCSRPEM(t, "ignored-cn")
	opts := initOpts(2, alice, bob, carol)
	opts.SOUsername = "secofficer"
	opts.SOCSRPEM = soCSR

	_, err := e.svc.Initialize(ctx, e.ca, opts)
	require.Error(t, err)
	require.True(t, backend.fired, "the fault came after the marker was written")
	// The rollback reseals the barrier, which then deletes nothing: the
	// marker stands in storage until the next initialization settles it.
	require.NotEmpty(t, backend.markerKeys(t))

	stale, err := backend.StorageBackend.Get(ctx, systemSOWriteOutstandingKey)
	require.NoError(t, err)

	// The node restarts onto storage holding that leftover marker (only
	// the marker: the attempt's other leftovers are not this test's), and
	// the operator initializes without an SO.
	backend2 := &failAfterMarkerBackend{StorageBackend: seal.NewMemoryBackend(), fired: true}
	require.NoError(t, backend2.StorageBackend.Put(ctx, systemSOWriteOutstandingKey, stale))
	e2 := &holderEnv{t: t, backend: backend2, ca: e.ca, pins: pintest.NewBackend(t), factors: e.factors}
	e2.barrier = shamirBarrier(t, e2.backend, 2, 3)
	e2.svc = e2.newService(e2.barrier)
	require.Equal(t, StateAwaitingInit, e2.svc.State())

	alice2 := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob2 := newHolder(t, "bob", seal.RoleSecurityOfficer)
	carol2 := newHolder(t, "carol", seal.RoleSecurityOfficer)
	_, err = e2.svc.Initialize(ctx, e2.ca, initOpts(2, alice2, bob2, carol2))
	require.NoError(t, err)
	require.Empty(t, backend2.markerKeys(t), "the fresh initialization removed the stale SO write-ahead marker")
	officers, err := e2.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	for _, o := range officers {
		for _, th := range []*testHolder{alice2, bob2, carol2} {
			if o.Username == th.username {
				th.thumb = o.CertThumbprint
			}
		}
	}
	e2.claim(seal.DomainUnseal, alice2)
	e2.claim(seal.DomainUnseal, bob2)
	e2.claim(seal.DomainUnseal, carol2)
	require.Equal(t, StateOperational, e2.svc.State(), "an SO-less ceremony completes once every holder claims")
}
