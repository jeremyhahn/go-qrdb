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

package embedded

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// This file is the awaiting-admin-credential counterpart of
// seal_gate_test.go's walk: that file proves every request method the
// embedded transport exports honours the SealProbe axis of t.gate; this one
// proves the SAME `t.gate` call also honours the AwaitingAdminCredentialProbe
// axis, for every method this security review (go-qrdb#548 item 3's
// composition with the full-surface widening) added authorization to.
// Without this walk, a method could call t.authorize with a fully-permissive
// identity -- the exact shape every behavioral test elsewhere in this
// package uses -- and still be answered as though the node's IAM were
// trustworthy while the bootstrap admin has no live certificate binding at
// all, which is precisely the state AwaitingAdminCredentialProbe exists to
// refuse.

// awaitingProbe is an AwaitingAdminCredentialProbe whose answer the test
// sets, mirroring seal_gate_test.go's gateProbe.
type awaitingProbe struct{ awaiting atomic.Bool }

func (p *awaitingProbe) AwaitingAdminCredential() bool { return p.awaiting.Load() }

func closedAwaitingProbe() *awaitingProbe {
	p := &awaitingProbe{}
	p.awaiting.Store(true)
	return p
}

// TestAwaitingAdminCredentialGate_RefusesEveryRequestOutsideTheAllowlist
// walks every *Transport request method exactly as
// TestSealGate_RefusesEveryRequestOutsideTheAllowlist does, but against
// WithAwaitingAdminCredentialProbe instead of WithSealProbe: while the node
// is awaiting admin credential, every request outside
// sealedwindow.EmbeddedAwaitingAdminCredential() (ceremony status and
// ceremony-initialize, the admin-credential retry) is refused with
// *authmw.AwaitingAdminCredentialError, the SAME typed refusal the network
// transports give, BEFORE any RBAC permission this review's items 1-10
// declared is ever consulted. The method set is walked by reflection, so a
// method added without the gate -- or one whose authorize() call runs before
// t.gate(), undoing the ordering this composition depends on -- fails here.
func TestAwaitingAdminCredentialGate_RefusesEveryRequestOutsideTheAllowlist(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{}, WithAwaitingAdminCredentialProbe(closedAwaitingProbe()))
	if err != nil {
		t.Fatal(err)
	}
	allow := sealedwindow.EmbeddedAwaitingAdminCredential()
	v := reflect.ValueOf(tr)
	gated := 0
	for _, m := range requestMethods() {
		if _, ok := allow[m.Name]; ok {
			continue
		}
		gated++
		var awaiting *authmw.AwaitingAdminCredentialError
		if err := callZero(v, m); !errors.As(err, &awaiting) {
			t.Errorf("%s while awaiting admin credential: err = %v, want *authmw.AwaitingAdminCredentialError", m.Name, err)
		}
	}
	if gated < minGatedMethods {
		t.Fatalf("gated %d request methods, want at least %d -- the walk found too few", gated, minGatedMethods)
	}
}

// TestAwaitingAdminCredentialGate_RefusesEverySubClientRequest extends the
// walk through every sub-client accessor, exactly as
// TestSealGate_RefusesEverySubClientRequest does for the seal axis: this
// review's item 2 (FactorClient, VolumePoolClient, PermissionClient,
// RBACClient.ListPermissions) and item 8 (SealStore*, reached through no
// sub-client but included here for symmetry with the request-method walk
// above) added authorization to methods a DIFFERENT concrete type carries,
// invisible to the walk above by construction -- the same blind spot the
// seal-gate walk was written to close.
func TestAwaitingAdminCredentialGate_RefusesEverySubClientRequest(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{}, WithAwaitingAdminCredentialProbe(closedAwaitingProbe()),
		WithAuditLogger(audit.NewNoopLogger()))
	if err != nil {
		t.Fatal(err)
	}
	allow := sealedwindow.EmbeddedAwaitingAdminCredential()
	trPtr := reflect.ValueOf(tr).Pointer()
	gated := 0
	for _, acc := range subClientAccessors() {
		client := reflect.ValueOf(tr).MethodByName(acc.Name).Call(nil)[0]
		if client.IsNil() {
			t.Errorf("%s() returned nil; the test must wire what it needs", acc.Name)
			continue
		}
		dyn := client.Elem()
		if dyn.Kind() == reflect.Pointer && dyn.Pointer() == trPtr {
			continue
		}
		for i := 0; i < dyn.NumMethod(); i++ {
			m := dyn.Type().Method(i)
			if ft := m.Type; ft.NumOut() == 0 || ft.Out(ft.NumOut()-1) != errorType {
				continue
			}
			name := acc.Name + "." + m.Name
			if _, ok := allow[name]; ok {
				continue
			}
			gated++
			var awaiting *authmw.AwaitingAdminCredentialError
			if err := callZeroBound(dyn.Method(i)); !errors.As(err, &awaiting) {
				t.Errorf("%s while awaiting admin credential: err = %v, want *authmw.AwaitingAdminCredentialError", name, err)
			}
		}
	}
	if gated < minGatedSubClientMethods {
		t.Fatalf("gated %d sub-client methods, want at least %d -- the walk found too few", gated, minGatedSubClientMethods)
	}
}

// TestAwaitingAdminCredentialGate_AllowlistNamesRealMethodsAndOpensWithTheProbe
// pins the allowlist to real methods, proves an allowlisted call reaches
// past the gate (CeremonyStatus, which needs no service to answer nil), and
// proves the probe gates both ways and composes with having no probe at
// all -- the three properties
// TestSealGate_AllowlistNamesRealMethods/TestSealGate_OpensWithTheProbe pin
// for the seal axis.
func TestAwaitingAdminCredentialGate_AllowlistNamesRealMethodsAndOpensWithTheProbe(t *testing.T) {
	typ := reflect.TypeFor[*Transport]()
	for name := range sealedwindow.EmbeddedAwaitingAdminCredential() {
		if _, ok := typ.MethodByName(name); !ok {
			t.Errorf("awaiting-admin-credential allowlist names %q, which *Transport does not have", name)
		}
	}

	probe := closedAwaitingProbe()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithAwaitingAdminCredentialProbe(probe))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Allowlisted: reaches past the gate while awaiting -- to whatever the
	// unconfigured ceremony service answers next (*NilServiceError), never
	// the awaiting refusal.
	var awaiting *authmw.AwaitingAdminCredentialError
	if _, err := tr.CeremonyStatus(ctx); errors.As(err, &awaiting) {
		t.Fatalf("CeremonyStatus while awaiting admin credential was refused as awaiting: %v", err)
	}

	// Not allowlisted: refused while awaiting.
	if err := tr.Put(ctx, "k", []byte("v")); !errors.As(err, &awaiting) {
		t.Fatalf("Put while awaiting admin credential: err = %v, want *authmw.AwaitingAdminCredentialError", err)
	}

	// The probe flips open: the identical request now reaches past the gate
	// (to whatever the missing KV service or RBAC answers next, never the
	// awaiting refusal).
	probe.awaiting.Store(false)
	if err := tr.Put(ctx, "k", []byte("v")); errors.As(err, &awaiting) {
		t.Fatalf("Put with the probe open was refused as awaiting: %v", err)
	}

	// No probe at all gates nothing on this axis.
	open, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatal(err)
	}
	if err := open.Put(ctx, "k", []byte("v")); errors.As(err, &awaiting) {
		t.Fatalf("Put with no awaiting probe was refused as awaiting: %v", err)
	}
}
