// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ceremony

import (
	"context"
	"crypto/x509"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// initOpts builds initialization options over holders.
func initOpts(threshold int, holders ...*testHolder) *InitializeOptions {
	opts := &InitializeOptions{SOPin: "so-pin-1234", UserPin: "user-pin-1234", Threshold: threshold, Enroller: "admin", Hostname: "db.example.com", AdminGenerate: true}
	for _, h := range holders {
		opts.Officers = append(opts.Officers, OfficerConfig{Username: h.username, Role: h.role, CSRPEM: h.csrPEM})
	}
	return opts
}

// TestInitialize_ShamirEnrolsHoldersAndGoesOperationalOnceAllClaim drives
// the initialization ceremony end to end.
func TestInitialize_ShamirEnrolsHoldersAndGoesOperationalOnceAllClaim(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 3)
	holders := []*testHolder{newHolder(t, "alice", seal.RoleSecurityOfficer), newHolder(t, "bob", seal.RoleSecurityOfficer), newHolder(t, "carol", seal.RoleCustodian)}

	res, err := e.svc.Initialize(ctx, e.ca, initOpts(2, holders...))
	require.NoError(t, err)
	assert.Equal(t, StateEnrolling, res.State)
	assert.Equal(t, 3, res.ShareCount)
	require.Len(t, res.Holders, 3)
	assert.NotEmpty(t, res.SPKIPin)
	assert.NotEmpty(t, res.TLSCertPEM)
	for i, h := range holders {
		assert.Equal(t, h.role, res.Holders[i].Role)
		h.recordCert(t, res.Holders[i].CertPEM)
	}
	assert.True(t, e.pins.SOPINSet(), "Initialize provisions the SO PIN")
	assert.True(t, e.pins.UserPINSet())

	_, err = e.svc.Initialize(ctx, e.ca, initOpts(2, holders...))
	assert.ErrorIs(t, err, ErrAlreadyInitialized)

	e.claim(seal.DomainUnseal, holders[0])
	e.claim(seal.DomainUnseal, holders[1])
	assert.Equal(t, StateEnrolling, e.svc.State())
	e.claim(seal.DomainUnseal, holders[2])
	assert.Equal(t, StateOperational, e.svc.State())

	pinned, err := e.svc.SPKIPin(ctx)
	require.NoError(t, err)
	assert.Equal(t, res.SPKIPin, pinned)
}

// TestInitialize_SingleAdmin pins the threshold < 2 path. go-qrdb#548: qrdb
// init brings the system barrier up itself for single-admin, so this needs
// a barrier that Initialize's credential path can actually bring up --
// newHolderEnv's own is Shamir-configured and can only be brought up through
// InitializeShamir.
func TestInitialize_SingleAdmin(t *testing.T) {
	svc := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, noneStrategyBarrier(t))
	res, err := svc.Initialize(context.Background(), testCA(t), initOpts(0))
	require.NoError(t, err)
	assert.Equal(t, StateOperational, res.State)
	assert.Empty(t, res.Holders)
}

// TestInitialize_RestoresStateAcrossRestart is the go-qrdb#560 review fix:
// "after a restart the ceremony state is lost" -- NewCeremonyService always
// started at StateAwaitingInit, so a fresh *CeremonyService over an
// ALREADY-initialized barrier (the shape every restart takes: the barrier
// and its records are durable, but this Go value is not) looked exactly
// like a brand-new, never-initialized ceremony. Covers both the
// single-admin path (operational immediately) and Shamir custody (where an
// in-progress enrolment must restore to StateEnrolling, not
// StateAwaitingInit -- which would let a caller replay Initialize over an
// already-enrolled roster -- and not StateOperational, which the claims
// have not yet proven).
func TestInitialize_RestoresStateAcrossRestart(t *testing.T) {
	t.Run("single-admin: operational immediately", func(t *testing.T) {
		barrier := noneStrategyBarrier(t)
		svc := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, barrier)
		_, err := svc.Initialize(context.Background(), testCA(t), initOpts(0))
		require.NoError(t, err)
		require.Equal(t, StateOperational, svc.State())

		restarted, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
		require.NoError(t, err)
		assert.Equal(t, StateOperational, restarted.State())
	})

	t.Run("shamir: never initialized stays awaiting", func(t *testing.T) {
		barrier := shamirBarrier(t, seal.NewMemoryBackend(), 2, 2)
		restarted, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, barrier, testLogger())
		require.NoError(t, err)
		assert.Equal(t, StateAwaitingInit, restarted.State())
	})

	t.Run("shamir: enrolling but not all claimed restores to enrolling", func(t *testing.T) {
		e := newHolderEnv(t, 2, 2)
		alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
		bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
		res, err := e.svc.Initialize(context.Background(), e.ca, initOpts(2, alice, bob))
		require.NoError(t, err)
		require.Equal(t, StateEnrolling, res.State)
		alice.recordCert(t, res.Holders[0].CertPEM)
		e.claim(seal.DomainUnseal, alice) // one of two holders claims; bob does not
		require.Equal(t, StateEnrolling, e.svc.State())

		restarted, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, e.barrier, testLogger())
		require.NoError(t, err)
		assert.Equal(t, StateEnrolling, restarted.State(),
			"an in-progress enrolment must restore to StateEnrolling, not StateAwaitingInit or StateOperational")
	})

	t.Run("shamir: all claimed restores to operational", func(t *testing.T) {
		e := newHolderEnv(t, 2, 2)
		alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
		bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
		res, err := e.svc.Initialize(context.Background(), e.ca, initOpts(2, alice, bob))
		require.NoError(t, err)
		alice.recordCert(t, res.Holders[0].CertPEM)
		bob.recordCert(t, res.Holders[1].CertPEM)
		e.claim(seal.DomainUnseal, alice)
		e.claim(seal.DomainUnseal, bob)
		require.Equal(t, StateOperational, e.svc.State())

		restarted, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, e.barrier, testLogger())
		require.NoError(t, err)
		assert.Equal(t, StateOperational, restarted.State())
	})
}

// TestInitialize_FailsClosedWithoutAPINBackend pins ruling 7.
func TestInitialize_FailsClosedWithoutAPINBackend(t *testing.T) {
	svc, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, shamirBarrier(t, seal.NewMemoryBackend(), 2, 2), testLogger())
	require.NoError(t, err)
	_, err = svc.Initialize(context.Background(), testCA(t), initOpts(0))
	assert.ErrorIs(t, err, pin.ErrBackendNotConfigured)
	assert.Equal(t, StateAwaitingInit, svc.State())
}

// TestInitialize_VerifiesPINsAlreadyProvisioned pins the one shared SO PIN
// path: barrier initialization and the ceremony both provision through
// pin.Provision, so the second verifies instead of failing on "already set",
// and a mismatched PIN is refused.
func TestInitialize_VerifiesPINsAlreadyProvisioned(t *testing.T) {
	svc := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, noneStrategyBarrier(t))
	require.NoError(t, pin.Provision(context.Background(), svc.pins, "so-pin-1234", "user-pin-1234"))
	_, err := svc.Initialize(context.Background(), testCA(t), initOpts(0))
	require.NoError(t, err)

	svc2 := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, noneStrategyBarrier(t))
	require.NoError(t, pin.Provision(context.Background(), svc2.pins, "other-so-pin", "user-pin-1234"))
	_, err = svc2.Initialize(context.Background(), testCA(t), initOpts(0))
	assert.Error(t, err, "a different SO PIN is refused, never overwritten")
}

// TestInitialize_ValidationRefusals covers the option checks.
func TestInitialize_ValidationRefusals(t *testing.T) {
	ctx := context.Background()
	// go-qrdb#548: qrdb init brings the system barrier up itself for the
	// single-admin path, which the final nil-CA case below reaches; that
	// needs a barrier Initialize's credential path can actually bring up,
	// not newHolderEnv's own Shamir-configured one.
	svc := newTestCeremonyService(t, &CeremonyConfig{DataDir: t.TempDir()}, noneStrategyBarrier(t))
	ca := testCA(t)
	a := newHolder(t, "alice", "")

	cases := map[string]struct {
		opts *InitializeOptions
		want error
	}{
		"nil options":    {nil, ErrInvalidSOPIN},
		"no SO PIN":      {&InitializeOptions{UserPin: "u"}, ErrInvalidSOPIN},
		"no user PIN":    {&InitializeOptions{SOPin: "s"}, ErrInvalidUserPIN},
		"no holders":     {initOpts(2), ErrNoOfficers},
		"threshold > N":  {initOpts(3, a), ErrInvalidThreshold},
		"empty username": {&InitializeOptions{SOPin: "s", UserPin: "u", Threshold: 2, Officers: []OfficerConfig{{}, {}}}, ErrMissingUsername},
		"duplicate":      {&InitializeOptions{SOPin: "s", UserPin: "u", Threshold: 2, Officers: []OfficerConfig{{Username: "a"}, {Username: "a"}}}, ErrDuplicateUsername},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Initialize(ctx, ca, tc.opts)
			assert.ErrorIs(t, err, tc.want)
		})
	}
	// go-qrdb#548: a nil CeremonyCA is no longer refused outright -- it
	// selects the BYO-TLS path, which here has no configured
	// CeremonyConfig.ServerCertPEM to pin, so it is refused with
	// *NoServerCertificateError instead of the old ErrNilCA. The
	// admin-credential selection must be a VALID BYOC certificate (the
	// only path available with ca == nil): AdminGenerate -- what initOpts
	// sets -- requires a CA and would be refused by admin-credential
	// validation before ever reaching the server-certificate check this
	// case is about (go-qrdb#548 review: B3 validates admin-credential
	// inputs first).
	adminLeaf, ierr := ca.IssueCertificate(ctx, &IssueCertRequest{
		CommonName: "admin", OrganizationalUnit: []string{"system", "administrator"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ValidityDays: 30,
	})
	require.NoError(t, ierr)
	caBundle, berr := ca.CABundle()
	require.NoError(t, berr)
	_, err := svc.Initialize(ctx, nil, &InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin",
		AdminCertPEM:     adminLeaf.CertPEM,
		AdminCertPoolPEM: caBundle,
	})
	var noCert *NoServerCertificateError
	assert.ErrorAs(t, err, &noCert)
}

// TestInitialize_HolderRegistrationFailureAborts pins that a bad holder
// aborts the ceremony before anything is distributed.
func TestInitialize_HolderRegistrationFailureAborts(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	opts := initOpts(2, newHolder(t, "alice", ""), newHolder(t, "bob", ""))
	opts.Officers[1].CSRPEM = []byte("garbage")
	_, err := e.svc.Initialize(context.Background(), e.ca, opts)
	assert.True(t, errorAs[*CSRParseError](err), err)
}

// TestInitialize_HolderRegistrationFailureAborts_NothingPersistedAndRetrySucceeds
// strengthens the sibling test above with go-qrdb#560 review B3's own
// assertion: a 2-of-2 init where officer 2's CSR is malformed refuses the
// WHOLE call with nothing persisted for EITHER officer -- not just the bad
// one -- and a corrected retry succeeds cleanly. Before B3's fix, officer 1
// (alice) was already signed and persisted by the time officer 2 (bob)'s
// malformed CSR was discovered, inside the per-officer enrollment loop;
// this proves the pre-flight (validateOfficerCredentials) now catches it
// before either officer is touched, and before pin.Provision, the TLS
// issue or issueAdminCredential run at all.
func TestInitialize_HolderRegistrationFailureAborts_NothingPersistedAndRetrySucceeds(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	opts := initOpts(2, alice, bob)
	opts.Officers[1].CSRPEM = []byte("garbage")

	_, err := e.svc.Initialize(ctx, e.ca, opts)
	assert.True(t, errorAs[*CSRParseError](err), err)
	assert.Equal(t, StateAwaitingInit, e.svc.State(), "a refused init must leave the ceremony awaiting, not partially initialized")
	assert.False(t, e.pins.SOPINSet(), "a refused init must not have provisioned the SO PIN either")

	officers, lerr := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, lerr)
	assert.Empty(t, officers, "neither officer -- not even alice, who would have enrolled before bob's malformed CSR was ever reached -- must be persisted")

	// A corrected retry, with bob's CSR fixed, must succeed cleanly: the
	// refusal above must not have left any state (a provisioned PIN, an
	// issued admin certificate, a half-enrolled alice) that would make
	// this retry behave any differently from a genuinely first attempt.
	opts.Officers[1].CSRPEM = bob.csrPEM
	res, err := e.svc.Initialize(ctx, e.ca, opts)
	require.NoError(t, err, "a corrected retry must succeed")
	assert.Equal(t, StateEnrolling, res.State)
	require.Len(t, res.Holders, 2)

	officers, lerr = e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, lerr)
	assert.Len(t, officers, 2, "both officers must be enrolled after the corrected retry")
}

// TestInitialize_DuplicateOfficerKeyWithinBatchRefusedBeforeEitherPersists
// covers the other half of go-qrdb#560 review B3's "duplicate keys" pre-
// flight: two officers submitting the SAME certificate key in ONE batch
// must be refused -- and, critically, refused before the FIRST of the two
// is ever enrolled. enrol's own duplicate check compares only against
// ALREADY-PERSISTED holders, so without the batch-wide pre-flight the
// first officer sharing a key would be signed and persisted, and only the
// second would trip the check -- leaving the first as orphaned partial
// state.
func TestInitialize_DuplicateOfficerKeyWithinBatchRefusedBeforeEitherPersists(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	opts := initOpts(2, alice, bob)
	opts.Officers[1].CSRPEM = alice.csrPEM // bob presents alice's own key

	_, err := e.svc.Initialize(ctx, e.ca, opts)
	assert.ErrorIs(t, err, ErrDuplicateHolderKey)
	assert.Equal(t, StateAwaitingInit, e.svc.State())

	officers, lerr := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, lerr)
	assert.Empty(t, officers, "alice must not be enrolled just because she was first in the batch")
}

// TestInitialize_SecondOfficerBadInputRefusesBeforeEitherPersists is the
// go-qrdb#560 re-review's B3 finding: validateOfficerCredentials's batch
// pre-flight used to carry only a PARTIAL copy of enrol's own checks --
// CSR/key kind/KEM/factors -- missing role validity, validPrincipalName and
// ErrSelfEnrolment. A batch where only the SECOND officer had a bad
// username, a bad role, or named itself as its own enroller slipped past
// the pre-flight entirely: enrol's own, fuller check caught it, but only
// after the FIRST officer in the loop was already signed and persisted,
// and a corrected retry then refused forever on "name already taken".
//
// validateOfficerEnrolment is now the ONE validator both enrol and the
// batch pre-flight call, so every one of these three bad-input shapes on
// the SECOND officer refuses the whole call with NOTHING persisted for
// either officer, and a corrected retry succeeds cleanly.
func TestInitialize_SecondOfficerBadInputRefusesBeforeEitherPersists(t *testing.T) {
	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)

	cases := []struct {
		name    string
		mutate  func(opts *InitializeOptions)
		wantErr error
	}{
		{
			name:    "bad username",
			mutate:  func(opts *InitializeOptions) { opts.Officers[1].Username = "bob smith!" },
			wantErr: nil, // *InvalidHolderNameError, checked via errorAs below
		},
		{
			name:    "bad role",
			mutate:  func(opts *InitializeOptions) { opts.Officers[1].Role = seal.HolderRole("bogus") },
			wantErr: ErrInvalidHolderRole,
		},
		{
			name:    "self-enrolment",
			mutate:  func(opts *InitializeOptions) { opts.Enroller = "bob" },
			wantErr: ErrSelfEnrolment,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			e := newHolderEnv(t, 2, 2)
			opts := initOpts(2, alice, bob)
			tc.mutate(opts)

			_, err := e.svc.Initialize(ctx, e.ca, opts)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.True(t, errorAs[*InvalidHolderNameError](err), "err = %v", err)
			}
			assert.Equal(t, StateAwaitingInit, e.svc.State(),
				"a refused init must leave the ceremony awaiting, not partially initialized")
			assert.False(t, e.pins.SOPINSet(), "a refused init must not have provisioned the SO PIN either")

			officers, lerr := e.svc.ListOfficers(ctx, seal.DomainUnseal)
			require.NoError(t, lerr)
			assert.Empty(t, officers, "alice must not be enrolled just because she was first in the batch")

			// A corrected retry must succeed cleanly: the refusal above must
			// not have left any state that would make this retry behave any
			// differently from a genuinely first attempt.
			retryOpts := initOpts(2, alice, bob)
			res, err := e.svc.Initialize(ctx, e.ca, retryOpts)
			require.NoError(t, err, "a corrected retry must succeed")
			assert.Equal(t, StateEnrolling, res.State)
			require.Len(t, res.Holders, 2)
		})
	}
}

// failAfterNCA wraps a real CeremonyCA and fails SignCSR starting on its
// (n+1)th call, simulating a genuine post-validation fault (a transient CA
// / HSM failure) that validateOfficerEnrolment cannot catch pre-flight
// because it never calls the CA at all.
type failAfterNCA struct {
	CeremonyCA
	n     int
	calls int
}

func (c *failAfterNCA) SignCSR(ctx context.Context, csrPEM []byte, opts *SignCSROptions) (*x509.Certificate, error) {
	c.calls++
	if c.calls > c.n {
		return nil, errors.New("simulated CA signing fault")
	}
	return c.CeremonyCA.SignCSR(ctx, csrPEM, opts)
}

// TestInitialize_OfficerSigningFaultRollsBackEarlierOfficersAndPIN is the
// go-qrdb#560 review B3 "transactional Initialize" half: a fault that
// survives validateOfficerEnrolment's pre-flight entirely -- the CA itself
// fails to sign the SECOND officer's certificate -- must not leave the
// FIRST officer persisted, nor the SO/User PINs provisioned, so a corrected
// retry (here, against a CA that works) succeeds cleanly.
func TestInitialize_OfficerSigningFaultRollsBackEarlierOfficersAndPIN(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	alice := newHolder(t, "alice", seal.RoleSecurityOfficer)
	bob := newHolder(t, "bob", seal.RoleSecurityOfficer)
	opts := initOpts(2, alice, bob)

	faultyCA := &failAfterNCA{CeremonyCA: e.ca, n: 1}
	_, err := e.svc.Initialize(ctx, faultyCA, opts)
	require.Error(t, err, "bob's simulated CA fault must refuse the whole call")
	assert.Equal(t, StateAwaitingInit, e.svc.State(),
		"a refused init must leave the ceremony awaiting, not partially initialized")
	assert.False(t, e.pins.SOPINSet(),
		"alice's successful enrolment must not leave the SO PIN provisioned once bob's enrolment is rolled back")
	assert.False(t, e.pins.UserPINSet())

	officers, lerr := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, lerr)
	assert.Empty(t, officers, "alice must be rolled back, not left as orphaned partial state")

	// A corrected retry, against a CA that actually works, must succeed
	// cleanly -- including re-using alice's own username and certificate,
	// which would be refused as already-taken had the rollback not run.
	retryOpts := initOpts(2, alice, bob)
	res, err := e.svc.Initialize(ctx, e.ca, retryOpts)
	require.NoError(t, err, "a corrected retry must succeed")
	assert.Equal(t, StateEnrolling, res.State)
	require.Len(t, res.Holders, 2)
}

// toggleFailBackend wraps a REAL seal.StorageBackend (unlike failingBackend
// in store_persistent_test.go, which is a stateless double), letting a test
// force every write to fail until cleared while every read still reflects
// genuinely persisted state. That is what makes "reconstruct a barrier over
// the same backend after the fault clears" behave like an actual restart
// against durable storage, rather than against a double that never really
// wrote anything.
type toggleFailBackend struct {
	seal.StorageBackend
	failWrites atomic.Bool
	writeErr   error
}

func (b *toggleFailBackend) Put(ctx context.Context, key string, data []byte) error {
	if b.failWrites.Load() {
		return b.writeErr
	}
	return b.StorageBackend.Put(ctx, key, data)
}

func (b *toggleFailBackend) PutIfAbsent(ctx context.Context, key string, data []byte) (bool, error) {
	if b.failWrites.Load() {
		return false, b.writeErr
	}
	return b.StorageBackend.PutIfAbsent(ctx, key, data)
}

// TestInitialize_InitializeShamirFailureRollsBackEveryOfficerAndPIN is the
// go-qrdb#560 review D1/D2 fix: InitializeShamir runs AFTER every officer in
// the batch has already registered and persisted (the lock is released
// first, since InitializeShamir calls back into this service). A failure
// there -- the barrier's own Shamir split, or persisting its result -- used
// to return directly, with none of rollbackOfficerInit's cleanup: every
// officer stayed enrolled, and their usernames stayed permanently
// unavailable, even though the call that was supposed to enroll them
// reported failure. Unlike the sibling OfficerSigningFault test above
// (which fails INSIDE the enrollment loop, on the second of two officers),
// this fails AFTER the loop has fully committed all three.
//
// InitializeShamir's own barrier-level contract (pkg/seal/shamir.go) is that
// a failed distribution needs a FRESH barrier instance to retry -- "the
// in-memory epoch barrier cannot leave the initialized state within the
// process" -- so the retry half of this test reconstructs a new
// *seal.StorageBarrier and *CeremonyService over the SAME backend and the
// SAME officer-registry DataDir, exactly as a real node restart would,
// rather than reusing the first attempt's own barrier object.
func TestInitialize_InitializeShamirFailureRollsBackEveryOfficerAndPIN(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("shamir distribute: disk full")
	backend := &toggleFailBackend{StorageBackend: seal.NewMemoryBackend(), writeErr: wantErr}
	backend.failWrites.Store(true)
	barrier := shamirBarrier(t, backend, 2, 3)

	dataDir := t.TempDir()
	pinStorage := pintest.NewStorage()
	pins, err := pin.NewSoftwareBackend(pinStorage, pintest.HashConfig())
	require.NoError(t, err)

	e := &holderEnv{t: t, backend: backend, ca: testCA(t), pins: pins, factors: testFactors(t)}
	e.barrier = barrier
	svc, err := NewCeremonyService(&CeremonyConfig{DataDir: dataDir}, barrier, testLogger())
	require.NoError(t, err)
	svc.SetCA(e.ca)
	svc.SetFactorRegistry(e.factors)
	svc.SetPINBackend(e.pins)
	e.svc = svc

	holders := []*testHolder{
		newHolder(t, "alice", seal.RoleSecurityOfficer),
		newHolder(t, "bob", seal.RoleSecurityOfficer),
		newHolder(t, "carol", seal.RoleCustodian),
	}
	_, err = e.svc.Initialize(ctx, e.ca, initOpts(2, holders...))
	require.ErrorIs(t, err, wantErr, "InitializeShamir's own failure must propagate unchanged")
	assert.Equal(t, StateAwaitingInit, e.svc.State(),
		"a refused init must leave the ceremony awaiting, not partially initialized, even when every officer had already committed")
	assert.False(t, e.pins.SOPINSet(),
		"every officer's successful enrolment must not leave the SO PIN provisioned once InitializeShamir's own failure is rolled back")
	assert.False(t, e.pins.UserPINSet())

	officers, lerr := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, lerr)
	assert.Empty(t, officers, "every officer -- all three had already committed before InitializeShamir ran -- must be rolled back")

	// go-qrdb#560 review D2: the PERSISTED PIN record, not only the
	// in-memory flag above, must be gone -- otherwise a FRESH backend
	// loading from the same pinStorage (the next assertion) would reload
	// the aborted PIN as if it were still provisioned.
	_, getErr := pinStorage.Get(ctx, "so-pin")
	assert.ErrorIs(t, getErr, pin.ErrNotFound, "the persisted SO PIN record must be deleted, not merely unreferenced in memory")
	_, getErr = pinStorage.Get(ctx, "user-pin")
	assert.ErrorIs(t, getErr, pin.ErrNotFound, "the persisted User PIN record must be deleted")

	// Cross-restart retry (go-qrdb#560 review D1/D2's own acceptance
	// wording): clear the fault, and reconstruct EVERY in-memory object a
	// real restart would -- a fresh barrier over the same backend, a fresh
	// PIN backend over the same storage, and a fresh ceremony service over
	// the same officer-registry DataDir -- then retry with a DIFFERENT
	// SO/User PIN. This only succeeds if BOTH the officer rollback (D1) and
	// the persisted PIN record's deletion (D2) actually took effect:
	// pin.Provision's own idempotent re-verify would otherwise refuse a
	// retry presenting a PIN that does not match whatever stayed
	// provisioned, and a non-rolled-back officer would refuse as
	// already-enrolled.
	backend.failWrites.Store(false)
	restartedBarrier := shamirBarrier(t, backend, 2, 3)
	restartedPins, err := pin.NewSoftwareBackend(pinStorage, pintest.HashConfig())
	require.NoError(t, err)
	restartedSvc, err := NewCeremonyService(&CeremonyConfig{DataDir: dataDir}, restartedBarrier, testLogger())
	require.NoError(t, err)
	restartedSvc.SetCA(e.ca)
	restartedSvc.SetFactorRegistry(e.factors)
	restartedSvc.SetPINBackend(restartedPins)

	retryOpts := initOpts(2, holders...)
	retryOpts.SOPin = "a-different-so-pin"
	retryOpts.UserPin = "a-different-user-pin"
	res, err := restartedSvc.Initialize(ctx, e.ca, retryOpts)
	require.NoError(t, err, "a cross-restart retry with a different PIN must succeed once the fault clears")
	assert.Equal(t, StateEnrolling, res.State)
	require.Len(t, res.Holders, 3)
}

// TestInitialize_RollbackNeverWipesAPINThatPredatesThisCall is the
// go-qrdb#591 re-review's non-blocking companion to the sibling test above:
// when the PIN backend ALREADY has the SO/User PIN set before this
// Initialize call ever reaches pin.Provision -- exactly the shape
// Provision's own doc comment describes ("barrier initialization and the
// initialization ceremony both call it, so whichever runs first sets the
// PINs and the other ... verifies them") -- a LATER failure in this same
// call (InitializeShamir's own fault, identical to the sibling test) must
// NOT roll back the PIN: this call never provisioned it, so it does not
// own it, and wiping it would destroy a record whatever set it first is
// still relying on. Unlike the sibling test, the PIN record must survive
// the rollback unchanged, not be deleted.
func TestInitialize_RollbackNeverWipesAPINThatPredatesThisCall(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("shamir distribute: disk full")
	backend := &toggleFailBackend{StorageBackend: seal.NewMemoryBackend(), writeErr: wantErr}
	backend.failWrites.Store(true)
	barrier := shamirBarrier(t, backend, 2, 3)

	dataDir := t.TempDir()
	pinStorage := pintest.NewStorage()
	pins, err := pin.NewSoftwareBackend(pinStorage, pintest.HashConfig())
	require.NoError(t, err)

	// Simulate "barrier initialization already ran first" (Provision's own
	// documented race): the PIN backend has the SO/User PIN set BEFORE this
	// Initialize call is ever made, by something other than this call.
	require.NoError(t, pins.SetSOPIN("", "pre-existing-so-pin"))
	require.NoError(t, pins.SetUserPIN("pre-existing-so-pin", "pre-existing-user-pin"))

	e := &holderEnv{t: t, backend: backend, ca: testCA(t), pins: pins, factors: testFactors(t)}
	e.barrier = barrier
	svc, err := NewCeremonyService(&CeremonyConfig{DataDir: dataDir}, barrier, testLogger())
	require.NoError(t, err)
	svc.SetCA(e.ca)
	svc.SetFactorRegistry(e.factors)
	svc.SetPINBackend(e.pins)
	e.svc = svc

	holders := []*testHolder{
		newHolder(t, "alice", seal.RoleSecurityOfficer),
		newHolder(t, "bob", seal.RoleSecurityOfficer),
		newHolder(t, "carol", seal.RoleCustodian),
	}
	// Initialize's own pin.Provision call presents the SAME PIN already
	// set, so it takes the verify branch, not the set branch.
	opts := initOpts(2, holders...)
	opts.SOPin = "pre-existing-so-pin"
	opts.UserPin = "pre-existing-user-pin"
	_, err = e.svc.Initialize(ctx, e.ca, opts)
	require.ErrorIs(t, err, wantErr, "InitializeShamir's own failure must propagate unchanged")

	assert.True(t, e.pins.SOPINSet(),
		"a PIN that existed before this Initialize call must survive rollback: this call only verified it, never provisioned it")
	assert.True(t, e.pins.UserPINSet())

	// The persisted record must also be untouched -- not merely the
	// in-memory flag.
	_, getErr := pinStorage.Get(ctx, "so-pin")
	assert.NoError(t, getErr, "the pre-existing persisted SO PIN record must not be deleted by this call's rollback")
	_, getErr = pinStorage.Get(ctx, "user-pin")
	assert.NoError(t, getErr, "the pre-existing persisted User PIN record must not be deleted by this call's rollback")

	// The ORIGINAL PIN must still verify: it must not have been rotated or
	// cleared by the aborted call.
	require.NoError(t, e.pins.VerifySOPIN("pre-existing-so-pin"))
}

// TestSignCSRInit_CAAssignsTheHolderIdentity pins the §6 fix: the CA assigns
// the certificate's identity from the request, never the CSR, and the SO PIN
// is verified by the PIN backend. go-qrdb#548: sign-csr issues HOLDER
// certificates only (so, custodian) and is PreOperationalOnly, so this now
// drives an M-of-N ceremony to StateEnrolling rather than single-admin's
// StateOperational.
func TestSignCSRInit_CAAssignsTheHolderIdentity(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	_, err := e.svc.Initialize(ctx, e.ca, initOpts(2, newHolder(t, "alice", seal.RoleSecurityOfficer), newHolder(t, "bob", seal.RoleSecurityOfficer)))
	require.NoError(t, err)
	h := newHolder(t, "ignored", "")

	res, err := e.svc.SignCSRInit(ctx, "operator-1", "so-pin-1234", h.csrPEM, "so")
	require.NoError(t, err)
	cert, err := mtls.ParseCertificatePEM(res.CertPEM)
	require.NoError(t, err)
	assert.Equal(t, "operator-1", cert.Subject.CommonName)
	assert.Equal(t, []string{tenantid.System, holderprofile.SecurityOfficer}, cert.Subject.OrganizationalUnit)
	assert.Empty(t, cert.URIs, "a ceremony certificate carries no SAN")
	_, isHolder := auth.HolderProfile(cert)
	assert.True(t, isHolder, "sign-csr issues HOLDER certificates only")

	_, err = e.svc.SignCSRInit(ctx, "operator-2", "wrong-pin", h.csrPEM, "so")
	assert.ErrorIs(t, err, pin.ErrPINInvalid)
}

// TestSignCSRInit_Refusals covers SignCSRInit's argument checks.
func TestSignCSRInit_Refusals(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	h := newHolder(t, "x", "")
	_, err := e.svc.SignCSRInit(ctx, "u", "so-pin-1234", h.csrPEM, "so")
	assert.ErrorIs(t, err, ErrNotInitializedOrEnrolling)
	// sign-csr is pre-operational only: the refusal names the one state it
	// runs in and never offers operational as an alternative.
	assert.Contains(t, err.Error(), "enrolling state")
	assert.NotContains(t, err.Error(), "operational state")

	_, err = e.svc.Initialize(ctx, e.ca, initOpts(2, newHolder(t, "alice", seal.RoleSecurityOfficer), newHolder(t, "bob", seal.RoleSecurityOfficer)))
	require.NoError(t, err)
	cases := map[string]struct {
		user, pin, role string
		csr             []byte
		want            error
	}{
		"no username": {"", "so-pin-1234", "so", h.csrPEM, ErrMissingUsername},
		"no SO PIN":   {"u", "", "so", h.csrPEM, ErrInvalidSOPIN},
		"no CSR":      {"u", "so-pin-1234", "so", nil, ErrMissingCSRPEM},
		"no role":     {"u", "so-pin-1234", "", h.csrPEM, ErrMissingRole},
		"bad role":    {"u", "so-pin-1234", "root", h.csrPEM, ErrInvalidRole},
		"bad CSR":     {"u", "so-pin-1234", "so", []byte("x"), ErrCSRSigningFailed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := e.svc.SignCSRInit(ctx, tc.user, tc.pin, tc.csr, tc.role)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// TestCeremonyService_Accessors covers the dependency accessors and the
// constructor's refusals.
func TestCeremonyService_Accessors(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	deps := e.svc.HolderDeps()
	assert.Same(t, e.ca, deps.CA)
	assert.Same(t, e.ca, e.svc.CA())
	assert.NotNil(t, e.svc.ThresholdRegistry())

	other := testCA(t)
	e.svc.SetCA(other)
	assert.Same(t, e.ca, e.svc.CA(), "the first CA wired is authoritative")
	e.svc.SetCA(nil)
	assert.Same(t, e.ca, e.svc.CA())

	b := shamirBarrier(t, seal.NewMemoryBackend(), 2, 2)
	_, err := NewCeremonyService(nil, b, testLogger())
	assert.ErrorIs(t, err, ErrNilConfig)
	_, err = NewCeremonyService(&CeremonyConfig{}, nil, testLogger())
	assert.ErrorIs(t, err, ErrNilBarrier)
	_, err = NewCeremonyService(&CeremonyConfig{}, b, nil)
	assert.ErrorIs(t, err, ErrNilLogger)

	fresh, err := NewCeremonyService(&CeremonyConfig{}, b, testLogger())
	require.NoError(t, err)
	_, err = fresh.SPKIPin(t.Context())
	assert.ErrorIs(t, err, ErrCANotInitialized)
}

// TestCeremonyService_RestartRestoresHoldersAfterUnseal pins the registry
// sync: after a restart the holders reappear once a quorum unseals, and a
// cleanup destroys every held envelope.
func TestCeremonyService_RestartRestoresHoldersAfterUnseal(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.initShamir("", a, b)

	b2, svc2 := e.restart(2, 2)
	list, err := svc2.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	assert.Empty(t, list, "holder records are behind the sealed barrier")

	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, a, "")
	require.NoError(t, err)
	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpUnseal, b, "")
	require.NoError(t, err)
	list, err = svc2.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	assert.Len(t, list, 2)

	// A rekey on the restarted node reaches the holders.
	_, err = b2.StartRekey(ctx, seal.DomainUnseal, seal.RekeyParams{Threshold: 2})
	require.NoError(t, err)
	_, err = e.submit(svc2, b2, seal.DomainUnseal, seal.OpRekey, a, "")
	require.NoError(t, err)
	out, err := e.submit(svc2, b2, seal.DomainUnseal, seal.OpRekey, b, "")
	require.NoError(t, err)
	require.True(t, out.Complete)

	list, err = svc2.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	for _, s := range list {
		assert.True(t, s.SharePending, s.Username)
	}
	svc2.Cleanup()
	list, err = svc2.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	for _, s := range list {
		assert.False(t, s.SharePending, "cleanup destroys held envelopes")
	}
}
