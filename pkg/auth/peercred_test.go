package auth

import (
	"context"
	"errors"
	"testing"
)

func u32(v uint32) *uint32 { return &v }

func TestPeerCredentialAuthenticator_NoCredentialIsUnauthenticated(t *testing.T) {
	a := NewPeerCredentialAuthenticator([]PeerCredentialMapping{{UID: u32(1000), Username: "alice"}})
	if a.Name() != MethodPeerCred {
		t.Fatalf("Name() = %q, want %q", a.Name(), MethodPeerCred)
	}
	_, err := a.Authenticate(context.Background())
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}

func TestPeerCredentialAuthenticator_UIDMappingBindsUser(t *testing.T) {
	a := NewPeerCredentialAuthenticator([]PeerCredentialMapping{{UID: u32(1000), TenantID: "t1", Username: "alice"}})
	ctx := WithPeerCredential(context.Background(), PeerCredential{UID: 1000, GID: 50, PID: 7})
	id, err := a.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != "alice" || id.TenantID != "t1" {
		t.Fatalf("identity = %q/%q, want alice/t1", id.Subject, id.TenantID)
	}
	if id.Metadata["peer_uid"] != "1000" || id.Metadata["peer_gid"] != "50" || id.Metadata["peer_pid"] != "7" {
		t.Fatalf("metadata = %v", id.Metadata)
	}
}

func TestPeerCredentialAuthenticator_UIDWinsOverGID(t *testing.T) {
	a := NewPeerCredentialAuthenticator([]PeerCredentialMapping{
		{GID: u32(50), Username: "group-user"},
		{UID: u32(1000), Username: "uid-user"},
	})
	id, err := a.Authenticate(WithPeerCredential(context.Background(), PeerCredential{UID: 1000, GID: 50}))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != "uid-user" {
		t.Fatalf("Subject = %q, want uid-user", id.Subject)
	}
}

func TestPeerCredentialAuthenticator_GIDMapping(t *testing.T) {
	a := NewPeerCredentialAuthenticator([]PeerCredentialMapping{
		{UID: u32(1), Username: "other"},
		{GID: u32(50), Username: "group-user"},
	})
	id, err := a.Authenticate(WithPeerCredential(context.Background(), PeerCredential{UID: 1000, GID: 50}))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != "group-user" {
		t.Fatalf("Subject = %q, want group-user", id.Subject)
	}
}

func TestPeerCredentialAuthenticator_UnmappedRefused(t *testing.T) {
	a := NewPeerCredentialAuthenticator([]PeerCredentialMapping{{UID: u32(1000), Username: "alice"}, {GID: u32(50), Username: "g"}})
	_, err := a.Authenticate(WithPeerCredential(context.Background(), PeerCredential{UID: 1001, GID: 51}))
	if !errors.Is(err, ErrPeerCredentialUnmapped) {
		t.Fatalf("err = %v, want ErrPeerCredentialUnmapped", err)
	}
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("err %T is not an *AuthError", err)
	}
}

func TestPeerCredentialAuthenticator_IdentityCarriesNoRoles(t *testing.T) {
	mappings := []PeerCredentialMapping{{UID: u32(1000), Username: "alice"}}
	a := NewPeerCredentialAuthenticator(mappings)
	mappings[0].Username = "mallory" // the authenticator holds a copy
	id, err := a.Authenticate(WithPeerCredential(context.Background(), PeerCredential{UID: 1000}))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if id.Subject != "alice" {
		t.Fatalf("Subject = %q, want alice (mapping slice copied)", id.Subject)
	}
	if len(id.Roles) != 0 || len(id.RoleBindings) != 0 || len(id.ScopePermissions) != 0 {
		t.Fatalf("identity carries roles %v bindings %v scopes %v", id.Roles, id.RoleBindings, id.ScopePermissions)
	}
}
