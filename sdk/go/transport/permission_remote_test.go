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

package transport

import (
	"context"
	"errors"
	"testing"
)

type fakePermissionEndpoints struct {
	catalog []PermissionInfo
	err     error
	calls   int
}

func (f *fakePermissionEndpoints) ListPermissionCatalog(context.Context) ([]PermissionInfo, error) {
	f.calls++
	return f.catalog, f.err
}

func TestPermissionCatalog_SortedWithScopes(t *testing.T) {
	cat, err := PermissionCatalog()
	if err != nil {
		t.Fatalf("PermissionCatalog: %v", err)
	}
	if len(cat) == 0 {
		t.Fatal("empty catalog")
	}
	for i, p := range cat {
		if p.Scope == "" || p.Resource == "" || p.Action == "" {
			t.Fatalf("entry %d incomplete: %+v", i, p)
		}
		if i > 0 && cat[i-1].Name >= p.Name {
			t.Fatalf("not sorted at %d: %q >= %q", i, cat[i-1].Name, p.Name)
		}
	}
}

func TestRemotePermissionClient_ListLookupLookupScope(t *testing.T) {
	cat, err := PermissionCatalog()
	if err != nil {
		t.Fatalf("PermissionCatalog: %v", err)
	}
	f := &fakePermissionEndpoints{catalog: cat}
	c := NewRemotePermissionClient(f)
	ctx := context.Background()

	got, err := c.List(ctx)
	if err != nil || len(got) != len(cat) {
		t.Fatalf("List = %d, %v; want %d", len(got), err, len(cat))
	}
	want := cat[0]
	info, err := c.Lookup(ctx, want.Name)
	if err != nil || info != want {
		t.Fatalf("Lookup(%q) = %+v, %v; want %+v", want.Name, info, err, want)
	}
	scope, err := c.LookupScope(ctx, want.Name)
	if err != nil || scope != want.Scope {
		t.Fatalf("LookupScope(%q) = %q, %v; want %q", want.Name, scope, err, want.Scope)
	}

	var nf *PermissionNotFoundError
	if _, err := c.Lookup(ctx, "nope:never"); !errors.As(err, &nf) || nf.Name != "nope:never" {
		t.Fatalf("Lookup unknown = %v; want PermissionNotFoundError", err)
	}
	if _, err := c.LookupScope(ctx, "nope:never"); !errors.As(err, &nf) {
		t.Fatalf("LookupScope unknown = %v; want PermissionNotFoundError", err)
	}
}

func TestRemotePermissionClient_EmptyScopeIsAnErrorNotSelf(t *testing.T) {
	f := &fakePermissionEndpoints{catalog: []PermissionInfo{{Name: "x:y", Resource: "x", Action: "y"}}}
	scope, err := NewRemotePermissionClient(f).LookupScope(context.Background(), "x:y")
	if err == nil || scope == PermissionScopeSelf {
		t.Fatalf("LookupScope empty scope = %q, %v; want error", scope, err)
	}
}

func TestRemotePermissionClient_EndpointErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	c := NewRemotePermissionClient(&fakePermissionEndpoints{err: boom})
	if _, err := c.List(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("List err = %v", err)
	}
	if _, err := c.Lookup(context.Background(), "kv:put"); !errors.Is(err, boom) {
		t.Fatalf("Lookup err = %v", err)
	}
}
