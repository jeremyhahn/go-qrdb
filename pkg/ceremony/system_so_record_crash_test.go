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

var errCrashedAfterCommit = errors.New("simulated crash: the process died after the initialization committed")

// postCommitCrashBackend models a process that dies the moment the barrier
// commits a Shamir initialization: once crashed, no system SO record write
// reaches storage.
type postCommitCrashBackend struct {
	seal.StorageBackend
	crashed bool
}

func (b *postCommitCrashBackend) Put(ctx context.Context, key string, value []byte) error {
	if b.crashed && strings.HasSuffix(key, pendingSystemSOKey) {
		return errCrashedAfterCommit
	}
	return b.StorageBackend.Put(ctx, key, value)
}

// crashOnCommit is the ceremony as the barrier's distributor, crashing the
// backend once the barrier reports the distribution committed.
type crashOnCommit struct {
	*CeremonyService
	backend *postCommitCrashBackend
}

func (c crashOnCommit) Committed(ctx context.Context, roster *seal.HolderRoster, removed []string) error {
	err := c.CeremonyService.Committed(ctx, roster, removed)
	c.backend.crashed = true
	return err
}

// TestInitialize_ShamirSystemSORecordSurvivesCrashAfterCommit: Distribute
// writes the system SO record before the M-of-N initialization commits, so a
// crash the moment it commits loses no SO: the restarted, unsealed ceremony
// reads the record back for first boot.
func TestInitialize_ShamirSystemSORecordSurvivesCrashAfterCommit(t *testing.T) {
	ctx := context.Background()
	backend := &postCommitCrashBackend{StorageBackend: seal.NewMemoryBackend()}
	e := &holderEnv{t: t, backend: backend, ca: testCA(t), pins: pintest.NewBackend(t), factors: testFactors(t)}
	e.barrier = shamirBarrier(t, e.backend, 2, 3)
	e.svc = e.newService(e.barrier)
	e.barrier.SetCeremonyDistributor(crashOnCommit{CeremonyService: e.svc, backend: backend})

	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	carol := newHolder(t, "carol", seal.RoleSecurityOfficer)
	soCSR, _ := generateCSRPEM(t, "ignored-cn")
	opts := initOpts(2, alice, bob, carol)
	opts.SOUsername = "secofficer"
	opts.SOCSRPEM = soCSR

	_, err := e.svc.Initialize(ctx, e.ca, opts)
	require.NoError(t, err, "nothing is written after the initialization commits")
	require.True(t, backend.crashed, "the crash came after the initialization committed")

	officers, err := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	for _, o := range officers {
		for _, h := range []*testHolder{alice, bob, carol} {
			if o.Username == h.username {
				h.thumb = o.CertThumbprint
			}
		}
	}
	e.claim(seal.DomainUnseal, alice)
	e.claim(seal.DomainUnseal, bob)

	// The process restarts: nothing in memory survives.
	backend.crashed = false
	b2, svc2 := e.restart(2, 3)
	require.Equal(t, StateEnrolling, svc2.State())
	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, alice, "")
	require.NoError(t, err)
	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, bob, "")
	require.NoError(t, err)
	require.False(t, b2.IsSealed())

	pending, err := svc2.PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, pending, "the SO record was durable before the initialization committed")
	require.Equal(t, "secofficer", pending.Username)
}
