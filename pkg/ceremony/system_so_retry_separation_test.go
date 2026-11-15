package ceremony

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// A single-admin init that fails after it unsealed a fresh barrier -- here
// at the TLS issuance -- reseals it, so the sealed window holds and startup
// cannot go on to first boot without the SO; the retry enrols the SO.
func TestInitialize_SingleAdminFaultAfterUnsealResealsAndRetryEnrolsSO(t *testing.T) {
	ctx := context.Background()
	mock := newMockCA(t)
	soCSR, _ := generateCSRPEM(t, "ignored-cn")
	opts := func() *InitializeOptions {
		return &InitializeOptions{
			SOPin:         "test-so-pin",
			UserPin:       "test-user-pin",
			AdminGenerate: true,
			SOUsername:    "secofficer",
			SOCSRPEM:      soCSR,
		}
	}
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	_, err := svc.Initialize(ctx, faultCA{CeremonyCA: mock, issueErr: errors.New("simulated TLS fault")}, opts())
	require.ErrorIs(t, err, ErrInitFailed)
	assert.True(t, svc.HoldsSealedWindow(ctx), "the failed init leaves the sealed window up")
	assert.Nil(t, svc.LastIssuedSystemSO())
	adminPEM, _ := svc.LastIssuedAdminCertPEM()
	assert.Empty(t, adminPEM)

	result, err := svc.Initialize(ctx, mock, opts())
	require.NoError(t, err, "the init is retryable")
	assert.Equal(t, StateOperational, result.State)
	assert.NotEmpty(t, result.SOCertPEM)
	pending, err := svc.PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, pending, "the retry enrolled the SO")
	assert.Equal(t, "secofficer", pending.Username)
	assert.Equal(t, result.SOCertPEM, pending.CertPEM)
}

// An M-of-N SO-persist retry hands back the admin certificate the failed
// call issued, so the separation check runs against that certificate: an
// initial admin key A, then a retry naming admin key C and SO key A, is
// refused and records no SO.
func TestInitialize_ShamirSystemSOPersistRetryRefusesTheCarriedAdminsKey(t *testing.T) {
	ctx := context.Background()
	backend := &systemSOWriteFailBackend{StorageBackend: seal.NewMemoryBackend(), armed: true}
	e := &holderEnv{t: t, backend: backend, ca: testCA(t), pins: pintest.NewBackend(t), factors: testFactors(t)}
	e.barrier = shamirBarrier(t, e.backend, 2, 3)
	e.svc = e.newService(e.barrier)

	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	carol := newHolder(t, "carol", seal.RoleSecurityOfficer)
	keyA, _ := generateCSRPEM(t, "ignored-a")
	keyB, _ := generateCSRPEM(t, "ignored-b")
	keyC, _ := generateCSRPEM(t, "ignored-c")
	opts := func(adminCSR, soCSR []byte) *InitializeOptions {
		o := initOpts(2, alice, bob, carol)
		o.AdminGenerate = false
		o.AdminCertPEM = nil
		o.AdminCSRPEM = adminCSR
		o.SOUsername = "secofficer"
		o.SOCSRPEM = soCSR
		return o
	}

	_, err := e.svc.Initialize(ctx, e.ca, opts(keyA, keyB))
	require.ErrorIs(t, err, errSystemSOWrite)
	require.False(t, backend.armed, "the fault hit the SO record write")

	_, err = e.svc.Initialize(ctx, e.ca, opts(keyC, keyA))
	var rejected *SystemSORejectedError
	require.ErrorAs(t, err, &rejected, "the SO shares the carried admin's key")

	pending, err := e.newService(e.barrier).PendingSystemSO(ctx)
	require.NoError(t, err)
	assert.Nil(t, pending, "no SO recorded")
	assert.Nil(t, e.svc.LastIssuedSystemSO())

	// The refusal consumed nothing: a retry naming a distinct SO key still
	// records the SO and hands back the carried admin.
	result, err := e.svc.Initialize(ctx, e.ca, opts(keyA, keyB))
	require.NoError(t, err)
	assert.NotEmpty(t, result.SOCertPEM)
	assert.NotEmpty(t, result.AdminCertPEM)
}
