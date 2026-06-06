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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func newSessionTransport(t *testing.T, issuer SessionIssuer) *Transport {
	t.Helper()
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithSessionIssuer(issuer),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func TestSessionClient_ResolveIdentity_Happy(t *testing.T) {
	issuer := newMemSessionIssuer()
	tr := newSessionTransport(t, issuer)

	id := &auth.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"User"}}
	bearer, _, err := issuer.Issue(context.Background(), id)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	got, err := tr.SessionClient().ResolveIdentity(context.Background(), bearer)
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if got.Subject != "alice" || got.TenantID != "acme" {
		t.Errorf("unexpected identity: %+v", got)
	}
}

func TestSessionClient_ResolveIdentity_Empty(t *testing.T) {
	tr := newSessionTransport(t, newMemSessionIssuer())
	_, err := tr.SessionClient().ResolveIdentity(context.Background(), "")
	if err == nil {
		t.Fatal("expected InvalidBearerError")
	}
	var ibe *transport.InvalidBearerError
	if !errors.As(err, &ibe) {
		t.Errorf("expected *transport.InvalidBearerError, got %T", err)
	}
}

func TestSessionClient_ResolveIdentity_Unknown(t *testing.T) {
	tr := newSessionTransport(t, newMemSessionIssuer())
	_, err := tr.SessionClient().ResolveIdentity(context.Background(), "bogus")
	if err == nil {
		t.Fatal("expected InvalidBearerError")
	}
}

func TestSessionClient_Revoke_Happy(t *testing.T) {
	issuer := newMemSessionIssuer()
	tr := newSessionTransport(t, issuer)

	id := &auth.Identity{Subject: "alice", TenantID: "acme"}
	bearer, _, err := issuer.Issue(context.Background(), id)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	sc := tr.SessionClient()
	if err := sc.Revoke(context.Background(), bearer); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, err = sc.ResolveIdentity(context.Background(), bearer)
	if err == nil {
		t.Fatal("expected ResolveIdentity to fail after Revoke")
	}
}

func TestSessionClient_Revoke_Empty(t *testing.T) {
	tr := newSessionTransport(t, newMemSessionIssuer())
	err := tr.SessionClient().Revoke(context.Background(), "")
	if err == nil {
		t.Fatal("expected InvalidBearerError")
	}
}

func TestSessionClient_MissingIssuer(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sc := tr.SessionClient()

	_, err = sc.ResolveIdentity(context.Background(), "any")
	if err == nil {
		t.Fatal("expected MissingSessionIssuerError")
	}
	var miss *MissingSessionIssuerError
	if !errors.As(err, &miss) {
		t.Errorf("expected *MissingSessionIssuerError, got %T", err)
	}

	err = sc.Revoke(context.Background(), "any")
	if err == nil {
		t.Fatal("expected MissingSessionIssuerError on Revoke")
	}
}
