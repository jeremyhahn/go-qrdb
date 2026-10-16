// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !integration

package node

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// principalRoles
// ---------------------------------------------------------------------------

func TestPrincipalRoles_RoleIDsOnly(t *testing.T) {
	u := &user.User{RoleIDs: []string{"admin", "ops"}}
	assert.Equal(t, []string{"admin", "ops"}, principalRoles(u))
}

func TestPrincipalRoles_BindingsAppendedAndDeduped(t *testing.T) {
	u := &user.User{
		RoleIDs: []string{"admin", "ops"},
		RoleBindings: []user.RoleBinding{
			{RoleID: "ops", TenantID: "t1"},          // duplicate of RoleIDs entry
			{RoleID: "tenant-admin", TenantID: "t1"}, // new
			{RoleID: "", TenantID: "t1"},             // empty: skipped
		},
	}
	// RoleIDs first, then unique binding refs in order; empties dropped.
	assert.Equal(t, []string{"admin", "ops", "tenant-admin"}, principalRoles(u))
}

func TestPrincipalRoles_Empty(t *testing.T) {
	assert.Nil(t, principalRoles(&user.User{}))
}

// ---------------------------------------------------------------------------
// buildLoginOpts / buildEmbeddedOpts wiring
// ---------------------------------------------------------------------------

// newMemoryNodeWithUser builds an unsealed standalone memory node (which
// always carries a User service via lifecycle.BuildServices) with all
// protocol servers disabled so no ports are bound.
func newMemoryNodeWithUser(t *testing.T, nodeOpts ...NodeOption) *Node {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	// In-memory primary storage; vector storage still requires a disk directory
	// so use t.TempDir() to keep all artifacts inside the test sandbox.
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(context.Background(), cfg, nil, nodeOpts...)
	require.NoError(t, err)
	require.NotNil(t, n)
	require.NotNil(t, n.Services().User, "memory node must expose a User service")
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func TestBuildEmbeddedOpts_NoUser_SkipsLoginWiring(t *testing.T) {
	// A node whose User service is nil must not wire the login ceremony and
	// must not error.
	n := &Node{}
	svcNode := newMemoryNodeWithUser(t).Services()
	// The same services with no User. A lifecycle.Node is never copied (it
	// owns its closers and the client trust pool slot), so this one is
	// assembled from the running node's handles instead.
	noUser := &lifecycle.Node{
		KV:               svcNode.KV,
		Admin:            svcNode.Admin,
		MVCC:             svcNode.MVCC,
		Batch:            svcNode.Batch,
		Transaction:      svcNode.Transaction,
		Lease:            svcNode.Lease,
		Index:            svcNode.Index,
		TimeSeries:       svcNode.TimeSeries,
		Watch:            svcNode.Watch,
		Lock:             svcNode.Lock,
		Database:         svcNode.Database,
		Vector:           svcNode.Vector,
		Backup:           svcNode.Backup,
		Embedding:        svcNode.Embedding,
		RAG:              svcNode.RAG,
		Role:             svcNode.Role,
		Barrier:          svcNode.Barrier,
		Tenant:           svcNode.Tenant,
		CertProv:         svcNode.CertProv,
		ReplicatedAudit:  svcNode.ReplicatedAudit,
		CertEnrollment:   svcNode.CertEnrollment,
		TenantOnboarding: svcNode.TenantOnboarding,
		Invitations:      svcNode.Invitations,
		DEK:              svcNode.DEK,
	}

	opts, err := n.buildEmbeddedOpts(noUser, nil, false, false)
	require.NoError(t, err)
	require.NotEmpty(t, opts)

	// Constructing a transport from these opts must succeed; FinishLogin must
	// report the resolver is missing (login not wired).
	tp, err := embedded.New(noUser.KV, noUser.Admin, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tp.Close() })

	_, err = tp.LoginClient().FinishLogin(context.Background(), transport.FinishLoginRequest{
		SessionID: "missing",
		Type:      transport.FactorTypePIV,
		LookupKey: []byte("x"),
	})
	var missing *embedded.MissingLoginResolverError
	require.ErrorAs(t, err, &missing)
}

// fakeSessionIssuer is a minimal in-memory SessionIssuer for the ceremony.
type fakeSessionIssuer struct {
	mu       sync.Mutex
	byBearer map[string]*auth.Identity
	seq      int
}

func newFakeSessionIssuer() *fakeSessionIssuer {
	return &fakeSessionIssuer{byBearer: make(map[string]*auth.Identity)}
}

func (f *fakeSessionIssuer) Issue(_ context.Context, id *auth.Identity) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	bearer := "bearer-" + id.Subject
	f.byBearer[bearer] = id
	return bearer, time.Now().Add(time.Hour), nil
}

func (f *fakeSessionIssuer) Resolve(_ context.Context, bearer string) (*auth.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byBearer[bearer]
	if !ok {
		return nil, &transport.InvalidBearerError{Reason: "unknown bearer"}
	}
	return id, nil
}

func (f *fakeSessionIssuer) Revoke(_ context.Context, bearer string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byBearer, bearer)
	return nil
}

// fakePIVVerifier accepts any non-empty signature for PIV factors.
type fakePIVVerifier struct{}

func (fakePIVVerifier) Type() transport.FactorType { return transport.FactorTypePIV }

func (fakePIVVerifier) Verify(_ context.Context, factor *auth.AuthFactor, ch transport.VerifyChallenge) error {
	if factor == nil || factor.RevokedAt != nil {
		return &auth.InvalidFactorError{Reason: "revoked or nil factor"}
	}
	if len(ch.Signature) == 0 {
		return &auth.InvalidFactorError{Reason: "empty signature"}
	}
	return nil
}

// TestBuildEmbeddedOpts_WiresFunctionalLoginCeremony proves the end-to-end
// ceremony works through buildEmbeddedOpts: the LoginResolver, UserAuthFactor
// service, injected SessionIssuer, and injected FactorVerifier all flow
// through, and the resolved Identity reflects the qrdb user mapping.
func TestBuildEmbeddedOpts_WiresFunctionalLoginCeremony(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	issuer := newFakeSessionIssuer()

	n := newMemoryNodeWithUser(t,
		WithSessionIssuer(issuer),
		WithLoginFactorVerifier(fakePIVVerifier{}),
	)
	svcNode := n.Services()

	// Create a user whose mapping we assert.
	u, err := svcNode.User.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
		TenantID: "tenant-a",
		RoleIDs:  []string{"admin"},
	})
	require.NoError(t, err)
	require.NotZero(t, u.ID)

	// Build the transport from the node's embedded opts (the path under test).
	opts, err := n.buildEmbeddedOpts(svcNode, nil, false, false)
	require.NoError(t, err)
	tp, err := embedded.New(svcNode.KV, svcNode.Admin, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tp.Close() })

	// Enroll a PIV factor for the user via the same transport.
	credential := []byte("piv-credential-id-1234567890")
	require.NoError(t, tp.FactorClient().Enroll(ctx, u.ID, transport.Factor{
		Type:       transport.FactorTypePIV,
		Label:      "yubikey",
		Credential: credential,
	}))

	// Begin + Finish the login ceremony.
	begin, err := tp.LoginClient().BeginLogin(ctx, transport.BeginLoginRequest{
		Type:     transport.FactorTypePIV,
		TenantID: "tenant-a",
	})
	require.NoError(t, err)
	require.NotEmpty(t, begin.SessionID)

	fin, err := tp.LoginClient().FinishLogin(ctx, transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypePIV,
		LookupKey: credential, // raw credential; resolver hashes it internally
		Signature: []byte("valid-proof"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, fin.Bearer, "SessionIssuer must mint a bearer")
	require.NotNil(t, fin.Identity)

	// Identity reflects the qrdb user.User -> auth.Principal mapping; the
	// Subject is the immutable user id, never the username.
	assert.Equal(t, auth.UserSubject(u.ID), fin.Identity.Subject)
	assert.Equal(t, "tenant-a", fin.Identity.TenantID)
	assert.Equal(t, []string{"admin"}, fin.Identity.Roles)

	// The bearer resolves back to the same identity via the issuer.
	resolved, err := tp.LoginClient().ResolveIdentity(ctx, fin.Bearer)
	require.NoError(t, err)
	assert.Equal(t, auth.UserSubject(u.ID), resolved.Subject)
}

// recordingHydrator is an embedded.IdentityHydrator that records the
// principals it hydrates and tries to name the account itself.
type recordingHydrator struct {
	principals []auth.Principal
}

func (h *recordingHydrator) Hydrate(_ context.Context, p *auth.Principal, _ *auth.AuthFactor) (*auth.Identity, error) {
	h.principals = append(h.principals, *p)
	return &auth.Identity{Subject: p.Email, TenantID: p.TenantID, Roles: []string{"hydrated"}}, nil
}

func TestWithIdentityHydrator_SubjectIsUserIDAcrossRenameAndEmailChange(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	hyd := &recordingHydrator{}
	n := newMemoryNodeWithUser(t,
		WithSessionIssuer(newFakeSessionIssuer()),
		WithLoginFactorVerifier(fakePIVVerifier{}),
		WithIdentityHydrator(hyd),
	)
	svcNode := n.Services()
	u, err := svcNode.User.CreateUser(ctx, &user.CreateUserRequest{
		Username: "alice",
		Email:    "alice@example.com",
		TenantID: "tenant-a",
	})
	require.NoError(t, err)

	opts, err := n.buildEmbeddedOpts(svcNode, nil, false, false)
	require.NoError(t, err)
	tp, err := embedded.New(svcNode.KV, svcNode.Admin, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tp.Close() })
	credential := []byte("piv-credential-id-rename")
	require.NoError(t, tp.FactorClient().Enroll(ctx, u.ID, transport.Factor{
		Type:       transport.FactorTypePIV,
		Label:      "yubikey",
		Credential: credential,
	}))

	login := func() *transport.Identity {
		t.Helper()
		begin, err := tp.LoginClient().BeginLogin(ctx, transport.BeginLoginRequest{
			Type:     transport.FactorTypePIV,
			TenantID: "tenant-a",
		})
		require.NoError(t, err)
		fin, err := tp.LoginClient().FinishLogin(ctx, transport.FinishLoginRequest{
			SessionID: begin.SessionID,
			Type:      transport.FactorTypePIV,
			LookupKey: credential,
			Signature: []byte("valid-proof"),
		})
		require.NoError(t, err)
		return fin.Identity
	}

	want := auth.UserSubject(u.ID)
	first := login()
	assert.Equal(t, want, first.Subject, "the Subject is the user id, not the hydrator's choice")
	assert.Equal(t, []string{"hydrated"}, first.Roles, "the injected hydrator assembles the roles")

	_, err = svcNode.User.UpdateUser(ctx, &user.UpdateUserRequest{ID: u.ID, Username: "alice2"})
	require.NoError(t, err)
	assert.Equal(t, want, login().Subject, "a rename must not change the Subject")

	_, err = svcNode.User.UpdateUser(ctx, &user.UpdateUserRequest{ID: u.ID, Email: "alice2@example.com"})
	require.NoError(t, err)
	assert.Equal(t, want, login().Subject, "an email change must not change the Subject")

	// The hydrator saw the account's current name and address each time.
	require.Len(t, hyd.principals, 3)
	assert.Equal(t, "alice", hyd.principals[0].Username)
	assert.Equal(t, "alice2", hyd.principals[1].Username)
	assert.Equal(t, "alice2@example.com", hyd.principals[2].Email)
}

func TestWithIdentityHydrator_SetsOption(t *testing.T) {
	hyd := &recordingHydrator{}
	o := &nodeOptions{}
	WithIdentityHydrator(hyd)(o)
	assert.Same(t, hyd, o.identityHydrator)
}

func TestWithIdentityHydrator_NilIsAccepted(t *testing.T) {
	o := &nodeOptions{}
	WithIdentityHydrator(nil)(o)
	assert.Nil(t, o.identityHydrator)
}

// TestBuildEmbeddedOpts_LoginUnknownFactor_NotFound proves the resolver is
// wired and rejects an unknown credential (the GetUser path is never reached
// because factor lookup fails first — the inverse user-not-found path is
// covered by the resolver's own unit tests in pkg/auth).
func TestBuildEmbeddedOpts_LoginUnknownFactor_NotFound(t *testing.T) {
	ctx := context.Background()
	issuer := newFakeSessionIssuer()
	n := newMemoryNodeWithUser(t, WithSessionIssuer(issuer))
	svcNode := n.Services()

	opts, err := n.buildEmbeddedOpts(svcNode, nil, false, false)
	require.NoError(t, err)
	tp, err := embedded.New(svcNode.KV, svcNode.Admin, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tp.Close() })

	begin, err := tp.LoginClient().BeginLogin(ctx, transport.BeginLoginRequest{
		Type: transport.FactorTypePIV,
	})
	require.NoError(t, err)

	_, err = tp.LoginClient().FinishLogin(ctx, transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypePIV,
		LookupKey: []byte("no-such-credential"),
	})
	require.Error(t, err, "login against an unknown factor must fail")
}

// ---------------------------------------------------------------------------
// NodeOption accessors
// ---------------------------------------------------------------------------

func TestWithSessionIssuer_SetsOption(t *testing.T) {
	issuer := newFakeSessionIssuer()
	o := &nodeOptions{}
	WithSessionIssuer(issuer)(o)
	assert.Same(t, issuer, o.sessionIssuer)
}

func TestWithSessionIssuer_NilIsAccepted(t *testing.T) {
	o := &nodeOptions{}
	WithSessionIssuer(nil)(o)
	assert.Nil(t, o.sessionIssuer)
}

func TestWithLoginFactorVerifier_Appends(t *testing.T) {
	o := &nodeOptions{}
	WithLoginFactorVerifier(fakePIVVerifier{})(o)
	WithLoginFactorVerifier(fakePIVVerifier{})(o)
	assert.Len(t, o.loginFactorVerifiers, 2)
}

func TestWithLoginFactorVerifier_NilIgnored(t *testing.T) {
	o := &nodeOptions{}
	WithLoginFactorVerifier(nil)(o)
	assert.Empty(t, o.loginFactorVerifiers)
}
