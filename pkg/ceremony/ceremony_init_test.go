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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// initOpts builds initialization options over holders.
func initOpts(threshold int, holders ...*testHolder) *InitializeOptions {
	opts := &InitializeOptions{SOPin: "so-pin-1234", UserPin: "user-pin-1234", Threshold: threshold, Enroller: "admin", Hostname: "db.example.com"}
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

// TestInitialize_SingleAdmin pins the threshold < 2 path.
func TestInitialize_SingleAdmin(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	res, err := e.svc.Initialize(context.Background(), e.ca, initOpts(0))
	require.NoError(t, err)
	assert.Equal(t, StateOperational, res.State)
	assert.Empty(t, res.Holders)
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
	e := newHolderEnv(t, 2, 2)
	require.NoError(t, pin.Provision(context.Background(), e.pins, "so-pin-1234", "user-pin-1234"))
	_, err := e.svc.Initialize(context.Background(), e.ca, initOpts(0))
	require.NoError(t, err)

	e2 := newHolderEnv(t, 2, 2)
	require.NoError(t, pin.Provision(context.Background(), e2.pins, "other-so-pin", "user-pin-1234"))
	_, err = e2.svc.Initialize(context.Background(), e2.ca, initOpts(0))
	assert.Error(t, err, "a different SO PIN is refused, never overwritten")
}

// TestInitialize_ValidationRefusals covers the option checks.
func TestInitialize_ValidationRefusals(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
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
			_, err := e.svc.Initialize(ctx, e.ca, tc.opts)
			assert.ErrorIs(t, err, tc.want)
		})
	}
	_, err := e.svc.Initialize(ctx, nil, initOpts(0))
	assert.ErrorIs(t, err, ErrNilCA)
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

// TestSignCSRInit_CAAssignsTheLoginIdentity pins the §6 fix for login
// certificates, and that the SO PIN is verified by the PIN backend.
func TestSignCSRInit_CAAssignsTheLoginIdentity(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	_, err := e.svc.Initialize(ctx, e.ca, initOpts(0))
	require.NoError(t, err)
	h := newHolder(t, "ignored", "")

	res, err := e.svc.SignCSRInit(ctx, "operator-1", "so-pin-1234", h.csrPEM, "admin")
	require.NoError(t, err)
	cert, err := mtls.ParseCertificatePEM(res.CertPEM)
	require.NoError(t, err)
	assert.Equal(t, "operator-1", cert.Subject.CommonName)
	assert.Equal(t, []string{tenantid.System, "administrator"}, cert.Subject.OrganizationalUnit)
	assert.Empty(t, cert.URIs, "a ceremony certificate carries no SAN")
	_, isHolder := auth.HolderProfile(cert)
	assert.False(t, isHolder, "a login certificate is never a holder certificate")

	_, err = e.svc.SignCSRInit(ctx, "operator-2", "wrong-pin", h.csrPEM, "admin")
	assert.ErrorIs(t, err, pin.ErrPINInvalid)
}

// TestSignCSRInit_Refusals covers SignCSRInit's argument checks.
func TestSignCSRInit_Refusals(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	h := newHolder(t, "x", "")
	_, err := e.svc.SignCSRInit(ctx, "u", "so-pin-1234", h.csrPEM, "admin")
	assert.ErrorIs(t, err, ErrNotInitializedOrEnrolling)

	_, err = e.svc.Initialize(ctx, e.ca, initOpts(0))
	require.NoError(t, err)
	cases := map[string]struct {
		user, pin, role string
		csr             []byte
		want            error
	}{
		"no username": {"", "so-pin-1234", "admin", h.csrPEM, ErrMissingUsername},
		"no SO PIN":   {"u", "", "admin", h.csrPEM, ErrInvalidSOPIN},
		"no CSR":      {"u", "so-pin-1234", "admin", nil, ErrMissingCSRPEM},
		"no role":     {"u", "so-pin-1234", "", h.csrPEM, ErrMissingRole},
		"bad role":    {"u", "so-pin-1234", "root", h.csrPEM, ErrInvalidRole},
		"bad CSR":     {"u", "so-pin-1234", "admin", []byte("x"), ErrCSRSigningFailed},
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
