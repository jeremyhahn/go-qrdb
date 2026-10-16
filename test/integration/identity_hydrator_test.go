// Copyright 2026 Jeremy Hahn
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

//go:build integration

// The login ceremony (BeginLogin / FinishLogin) is served by the embedded
// transport only: the network transports run no ceremony and take every
// request's identity from the authenticator chain. A hydrator is a Go value a
// composer passes to sdk/go/node, so it cannot reach a spawned qrdbd; these
// tests boot the node in process, the way a composer does, and drive the
// ceremony through the node's own SDK client.

package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// hydratorSessionIssuer is an in-memory embedded.SessionIssuer: qrdb owns no
// session store, so a composer always supplies one.
type hydratorSessionIssuer struct {
	mu       sync.Mutex
	byBearer map[string]*auth.Identity
}

var _ embedded.SessionIssuer = (*hydratorSessionIssuer)(nil)

func (s *hydratorSessionIssuer) Issue(_ context.Context, identity *auth.Identity) (string, time.Time, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	bearer := hex.EncodeToString(buf)
	s.mu.Lock()
	s.byBearer[bearer] = identity
	s.mu.Unlock()
	return bearer, time.Now().Add(time.Hour), nil
}

func (s *hydratorSessionIssuer) Resolve(_ context.Context, bearer string) (*auth.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byBearer[bearer]
	if !ok {
		return nil, &transport.InvalidBearerError{}
	}
	return id, nil
}

func (s *hydratorSessionIssuer) Revoke(_ context.Context, bearer string) error {
	s.mu.Lock()
	delete(s.byBearer, bearer)
	s.mu.Unlock()
	return nil
}

// namingHydrator tries to name the account by returning the username as the
// Subject, and counts its runs. The login Subject is the immutable user id
// (#670), bound after the hydrator runs, so the name it returns must never
// reach the session. It uses the sdk aliases only, as a composer importing no
// pkg/* would.
type namingHydrator struct {
	calls atomic.Int32
}

var _ embedded.IdentityHydrator = (*namingHydrator)(nil)

func (h *namingHydrator) Hydrate(_ context.Context, principal *sdk.Principal, _ *sdk.AuthFactor) (*sdk.Identity, error) {
	h.calls.Add(1)
	return &sdk.Identity{
		Subject:  principal.Username,
		TenantID: principal.TenantID,
		Roles:    append([]string(nil), principal.Roles...),
	}, nil
}

// hydratorLogin boots an in-process standalone node with auth enabled and the
// given node options, enrolls a PIV factor for a fresh user "hydrator-alice",
// and completes the login ceremony through the node's own SDK client. It
// returns the user, the ceremony's response and the client.
func hydratorLogin(t *testing.T, opts ...node.NodeOption) (*user.User, *transport.FinishLoginResponse, *sdk.Client) {
	t.Helper()
	ctx := context.Background()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	issuer := &hydratorSessionIssuer{byBearer: map[string]*auth.Identity{}}
	n, err := node.NewStandaloneNode(ctx, cfg, nil, append([]node.NodeOption{node.WithSessionIssuer(issuer)}, opts...)...)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	svc := n.Services()
	enrollRole, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "hydrator-factor-enroller",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.UserAuthFactorsAdd},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "hydrator-enroller",
		TenantID:             tenantid.System,
		RoleIDs:              []string{enrollRole.ID},
	}); err != nil {
		t.Fatalf("CreateUser(enroller): %v", err)
	}
	alice, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "hydrator-alice",
		TenantID:             tenantid.System,
	})
	if err != nil {
		t.Fatalf("CreateUser(alice): %v", err)
	}

	client := n.Client()
	enrollerCtx := auth.WithIdentity(ctx, &auth.Identity{
		Subject:  "hydrator-enroller",
		TenantID: tenantid.System,
		Roles:    []string{enrollRole.ID},
	})
	credential := []byte("piv-credential-hydrator-integration")
	if err := client.Factors().Enroll(enrollerCtx, alice.ID, transport.Factor{
		Type:       transport.FactorTypePIV,
		Label:      "yubikey",
		Credential: credential,
	}); err != nil {
		t.Fatalf("Factors().Enroll: %v", err)
	}

	begin, err := client.Login().BeginLogin(ctx, transport.BeginLoginRequest{
		Type:     transport.FactorTypePIV,
		TenantID: tenantid.System,
	})
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	fin, err := client.Login().FinishLogin(ctx, transport.FinishLoginRequest{
		SessionID: begin.SessionID,
		Type:      transport.FactorTypePIV,
		LookupKey: credential,
	})
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if fin.Identity == nil || fin.Bearer == "" {
		t.Fatalf("FinishLogin returned no identity or bearer: %+v", fin)
	}
	return alice, fin, client
}

// TestIdentityHydrator_Embedded_HydratorCannotNameTheAccount proves a hydrator
// passed to sdk/go/node is the one the embedded login ceremony runs, and that
// it cannot choose the Subject: the ceremony's identity, and the one its bearer
// resolves to, carry the immutable user id, not the username it returned.
func TestIdentityHydrator_Embedded_HydratorCannotNameTheAccount(t *testing.T) {
	h := &namingHydrator{}
	alice, fin, client := hydratorLogin(t, node.WithIdentityHydrator(h))

	if got := h.calls.Load(); got != 1 {
		t.Fatalf("hydrator ran %d times, want once: the node's hydrator is not the one the ceremony runs", got)
	}
	assertLoginSubjectIsUserID(t, alice, fin, client)
}

// TestIdentityHydrator_Embedded_DefaultSubjectIsUserID pins the default a node
// without WithIdentityHydrator keeps: the login Subject is the user id too.
func TestIdentityHydrator_Embedded_DefaultSubjectIsUserID(t *testing.T) {
	alice, fin, client := hydratorLogin(t)
	assertLoginSubjectIsUserID(t, alice, fin, client)
}

// assertLoginSubjectIsUserID checks the ceremony's identity and the identity
// its bearer resolves to both carry alice's user id as the Subject.
func assertLoginSubjectIsUserID(t *testing.T, alice *user.User, fin *transport.FinishLoginResponse, client *sdk.Client) {
	t.Helper()
	want := auth.UserSubject(alice.ID)
	if fin.Identity.Subject != want {
		t.Fatalf("login Subject = %q, want the user id %q", fin.Identity.Subject, want)
	}
	if fin.Identity.TenantID != tenantid.System {
		t.Fatalf("login TenantID = %q, want %q", fin.Identity.TenantID, tenantid.System)
	}
	resolved, err := client.Login().ResolveIdentity(context.Background(), fin.Bearer)
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if resolved.Subject != want {
		t.Fatalf("bearer resolves to Subject %q, want %q", resolved.Subject, want)
	}
}
