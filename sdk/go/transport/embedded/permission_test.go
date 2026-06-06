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

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newPermissionTestTransport returns an embedded Transport configured
// with no auth-related services. Permission operations are stateless
// projections over the global permission registry, so this is enough
// to exercise the client.
func newPermissionTestTransport(t *testing.T) *Transport {
	t.Helper()
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("failed to create transport: %v", err)
	}
	return tr
}

func TestPermissionClient_List_ReturnsRegistry(t *testing.T) {
	tr := newPermissionTestTransport(t)
	pc := tr.PermissionClient()

	got, err := pc.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := permission.All()
	if len(got) != len(want) {
		t.Fatalf("expected %d permissions, got %d", len(want), len(got))
	}

	// The List output is sorted by name; spot-check a couple of well-
	// known permissions to confirm projection.
	gotByName := make(map[string]transport.PermissionInfo, len(got))
	for _, p := range got {
		gotByName[p.Name] = p
	}
	if _, ok := gotByName["kv:put"]; !ok {
		t.Error("expected kv:put in the result set")
	}
	if info, ok := gotByName["tenants:list:cross"]; !ok {
		t.Error("expected tenants:list:cross in the result set")
	} else if info.Scope != transport.PermissionScopeCross {
		t.Errorf("expected tenants:list:cross to be cross-scope, got %q", info.Scope)
	}
}

func TestPermissionClient_List_ClosedTransport(t *testing.T) {
	tr := newPermissionTestTransport(t)
	pc := tr.PermissionClient()

	if err := tr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err := pc.List(context.Background())
	if err == nil {
		t.Fatal("expected error from closed transport, got nil")
	}
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Errorf("expected *transport.ClosedError, got %T", err)
	}
}

func TestPermissionClient_Lookup_Found(t *testing.T) {
	tr := newPermissionTestTransport(t)
	pc := tr.PermissionClient()

	got, err := pc.Lookup(context.Background(), "kv:put")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "kv:put" {
		t.Errorf("expected Name=kv:put, got %q", got.Name)
	}
	if got.Resource != "kv" || got.Action != "put" {
		t.Errorf("expected Resource=kv Action=put, got %s/%s", got.Resource, got.Action)
	}
	if got.Scope != transport.PermissionScopeSelf {
		t.Errorf("expected ScopeSelf for kv:put, got %q", got.Scope)
	}
}

func TestPermissionClient_Lookup_NotFound(t *testing.T) {
	tr := newPermissionTestTransport(t)
	pc := tr.PermissionClient()

	_, err := pc.Lookup(context.Background(), "does:not:exist")
	if err == nil {
		t.Fatal("expected PermissionNotFoundError, got nil")
	}
	var pne *transport.PermissionNotFoundError
	if !errors.As(err, &pne) {
		t.Fatalf("expected *transport.PermissionNotFoundError, got %T: %v", err, err)
	}
	if pne.Name != "does:not:exist" {
		t.Errorf("expected Name=does:not:exist, got %q", pne.Name)
	}
}

func TestPermissionClient_LookupScope_Cross(t *testing.T) {
	tr := newPermissionTestTransport(t)
	pc := tr.PermissionClient()

	scope, err := pc.LookupScope(context.Background(), "tenants:list:cross")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scope != transport.PermissionScopeCross {
		t.Errorf("expected ScopeCross, got %q", scope)
	}
}

func TestPermissionClient_LookupScope_SelfDefault(t *testing.T) {
	tr := newPermissionTestTransport(t)
	pc := tr.PermissionClient()

	// Unregistered names default to ScopeSelf without error.
	scope, err := pc.LookupScope(context.Background(), "fictitious:permission")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scope != transport.PermissionScopeSelf {
		t.Errorf("expected ScopeSelf for unknown permission, got %q", scope)
	}
}
