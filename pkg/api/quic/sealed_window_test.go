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

package quic

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

func TestSealedWindowAllowed_AllowlistedMethods(t *testing.T) {
	for _, method := range []string{
		"barrier.Initialize", "barrier.Unseal",
		"barrier.ShamirInitialize",
		"barrier.QuorumBegin", "barrier.QuorumSubmit",
		"ceremony.Initialize",
	} {
		if !sealedWindowAllowed(method) {
			t.Errorf("expected %q to be allowed while sealed", method)
		}
	}
}

// TestSealedWindowAllowed_TrimmedMethods pins the verbs removed from the
// allowlist: anonymous on the boot-time listener, and refused by the
// service while sealed or served post-unseal to authenticated callers.
func TestSealedWindowAllowed_TrimmedMethods(t *testing.T) {
	for _, method := range []string{
		"barrier.OfficerList", "barrier.RecoveryDelete", "barrier.RecoveryGenerate",
		"barrier.RecoveryStatus", "barrier.Rekey", "barrier.ShamirVerify",
	} {
		if sealedWindowAllowed(method) {
			t.Errorf("expected %q to be rejected while sealed", method)
		}
	}
}

// TestSealedWindowRefusal covers the gate in both modes: the regular gate
// passes everything once unsealed, and the boot-time listener's gate
// refuses everything once unsealed, allowlisted methods included.
func TestSealedWindowRefusal(t *testing.T) {
	probe := &fakeSealedProbe{sealed: true}
	regular := newTestServer(&mockKVService{}, &mockAdminService{}, WithSealedWindowProbe(probe))
	listener := newTestServer(&mockKVService{}, &mockAdminService{}, WithSealedWindowListener(probe))
	none := newTestServer(&mockKVService{}, &mockAdminService{})

	if _, refused := sealedWindowGateRefusal(context.Background(), "kv.Put", none.sealedWindowProbe, none.sealedWindowListener); refused {
		t.Error("no gate installed: kv.Put refused")
	}
	for _, srv := range []*Server{regular, listener} {
		if _, refused := sealedWindowGateRefusal(context.Background(), "barrier.Unseal", srv.sealedWindowProbe, srv.sealedWindowListener); refused {
			t.Error("sealed: barrier.Unseal refused")
		}
		for _, method := range []string{"kv.Put", "admin.Health", "admin.Status"} {
			r, refused := sealedWindowGateRefusal(context.Background(), method, srv.sealedWindowProbe, srv.sealedWindowListener)
			assertQUICSealedRefusal(t, "sealed: "+method, r, refused)
		}
	}
	// Barrier status is the initialized flag of the sealed window's listener,
	// served before and after the first boot; a regular gate refuses it.
	if _, refused := sealedWindowGateRefusal(context.Background(), "barrier.Status", listener.sealedWindowProbe, listener.sealedWindowListener); refused {
		t.Error("listener, enrollment closed: barrier.Status refused")
	}
	r, refused := sealedWindowGateRefusal(context.Background(), "barrier.Status", regular.sealedWindowProbe, regular.sealedWindowListener)
	assertQUICSealedRefusal(t, "regular, enrollment closed: barrier.Status", r, refused)

	// Officer registration is a first-boot verb: both gates refuse it once
	// enrollment has closed, with the same sealed refusal as any other call.
	for name, srv := range map[string]*Server{"listener": listener, "regular": regular} {
		r, refused := sealedWindowGateRefusal(context.Background(), "barrier.OfficerRegister", srv.sealedWindowProbe, srv.sealedWindowListener)
		assertQUICSealedRefusal(t, name+", enrollment closed: barrier.OfficerRegister", r, refused)
	}
	probe.enrollment = true
	for _, method := range []string{"barrier.RecoveryOfficerRegister", "barrier.Status"} {
		if _, refused := sealedWindowGateRefusal(context.Background(), method, listener.sealedWindowProbe, listener.sealedWindowListener); refused {
			t.Errorf("listener, enrollment open: %s refused", method)
		}
	}
	r, refused = sealedWindowGateRefusal(context.Background(), "barrier.Status", regular.sealedWindowProbe, regular.sealedWindowListener)
	assertQUICSealedRefusal(t, "regular, enrollment open: barrier.Status", r, refused)

	probe.sealed = false
	if _, refused := sealedWindowGateRefusal(context.Background(), "kv.Put", regular.sealedWindowProbe, regular.sealedWindowListener); refused {
		t.Error("regular gate, unsealed: kv.Put refused")
	}
	for _, method := range []string{"barrier.Unseal", "kv.Put"} {
		r, refused := sealedWindowGateRefusal(context.Background(), method, listener.sealedWindowProbe, listener.sealedWindowListener)
		if !refused || r.payload.Message != sealedwindow.ClosedMessage {
			t.Errorf("listener gate, unsealed: %s = (%+v, %v), want (%q, true)", method, r, refused, sealedwindow.ClosedMessage)
		}
	}
}

// assertQUICSealedRefusal checks that a gate refused with the typed sealed
// refusal: StatusUnavailable, the sealed message, and error-frame info in
// the qrdb domain with reason SEALED.
func assertQUICSealedRefusal(t *testing.T, what string, r sealedWindowRefusal, refused bool) {
	t.Helper()
	if !refused || r.status != wire.StatusUnavailable || r.payload.Message != sealedwindow.SealedMessage {
		t.Errorf("%s = (%+v, %v), want refused Unavailable %q", what, r, refused, sealedwindow.SealedMessage)
		return
	}
	if info := r.payload.Info; info == nil || info.Domain != sealedwindow.ErrorDomain || info.Reason != sealedwindow.ReasonSealed {
		t.Errorf("%s: info = %+v, want {%s %s}", what, info, sealedwindow.ErrorDomain, sealedwindow.ReasonSealed)
	}
}

// stateSealedProbe reports the two-valued seal state of sealedwindow.StateProbe.
type stateSealedProbe struct{ sealed, unconfirmed bool }

func (p stateSealedProbe) IsSealed() bool                        { return p.sealed }
func (p stateSealedProbe) SealState() (sealed, unconfirmed bool) { return p.sealed, p.unconfirmed }

var _ sealedwindow.StateProbe = stateSealedProbe{}

// TestSealedWindowRefusal_UnconfirmedIsNotSealed: a regular gate on a node
// that cannot confirm the cluster seal state refuses with
// SEAL_STATE_UNCONFIRMED, never the sealed refusal, and still serves the
// allowlist -- barrier status included, which reports the node unsealed.
func TestSealedWindowRefusal_UnconfirmedIsNotSealed(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithSealedWindowProbe(stateSealedProbe{sealed: true, unconfirmed: true}))
	// An unconfirmed node is not sealed, so it refuses as unconfirmed; like a
	// held node it answers no status question, barrier status included.
	for _, method := range []string{"kv.Put", "barrier.Status"} {
		r, refused := sealedWindowGateRefusal(context.Background(), method, srv.sealedWindowProbe, srv.sealedWindowListener)
		if !refused || r.status != wire.StatusUnavailable {
			t.Fatalf("%s = (%+v, %v), want Unavailable", method, r, refused)
		}
		if want := (&clusterseal.SealStateUnconfirmedError{}).Error(); r.payload.Message != want {
			t.Errorf("%s: message = %q, want %q", method, r.payload.Message, want)
		}
		if r.payload.Info == nil || r.payload.Info.Reason != replicaforward.ReasonSealStateUnconfirmed || r.payload.Info.Domain != replicaforward.ErrorDomain {
			t.Errorf("%s: info = %+v, want %s/%s", method, r.payload.Info, replicaforward.ErrorDomain, replicaforward.ReasonSealStateUnconfirmed)
		}
	}
	if _, refused := sealedWindowGateRefusal(context.Background(), "barrier.Unseal", srv.sealedWindowProbe, srv.sealedWindowListener); refused {
		t.Error("unconfirmed: barrier.Unseal refused")
	}
}

func TestSealedWindowAllowed_NonAllowlistedMethods(t *testing.T) {
	for _, method := range []string{
		"kv.Put", "kv.Get", "db.Create", "sql.Query", "user.Create",
		// A sealed node answers no health or status question but barrier
		// status, the pre-init answer TestSealedWindowRefusal covers.
		"admin.Health", "admin.Status", "ceremony.Status",
		// Holder administration needs an unsealed barrier.
		"barrier.RekeyStart", "barrier.OfficerRevoke", "barrier.Seal",
	} {
		if sealedWindowAllowed(method) {
			t.Errorf("expected %q to be rejected while sealed", method)
		}
	}
}

// TestSealedWindowAllowlist_EveryMethodIsDispatchable proves the allowlist
// names only methods the server actually serves. An allowlisted name with no
// handler is a hole the sealed window keeps open for nothing, and it hides
// the real method when a name drifts.
func TestSealedWindowAllowlist_EveryMethodIsDispatchable(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	for method := range sealedwindow.QUICAllowlist() {
		if _, ok := srv.handlers[method]; !ok {
			t.Errorf("sealed-window method %q has no dispatch handler", method)
		}
	}
}

// fakeSealedProbe is a deterministic sealedWindowProbe test double.
type fakeSealedProbe struct {
	sealed, enrollment bool
	sealedwindow.Admission
}

func (f *fakeSealedProbe) EnrollmentOpen(context.Context) bool { return f.enrollment }

func (f *fakeSealedProbe) IsSealed() bool { return f.sealed }

func TestWithSealedWindowProbe_Configures(t *testing.T) {
	probe := &fakeSealedProbe{sealed: true}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithSealedWindowProbe(probe))
	if srv.sealedWindowProbe == nil {
		t.Fatal("expected sealedWindowProbe to be set")
	}
	if !srv.sealedWindowProbe.IsSealed() {
		t.Fatal("expected probe to report sealed")
	}
}

func TestWithSealedWindowProbe_NilByDefault(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	if srv.sealedWindowProbe != nil {
		t.Fatal("expected sealedWindowProbe to be nil by default")
	}
}

// TestSealedWindowRefusal_ClusterUnsealIsNeverAnonymous: cluster unseal passes
// the regular gate while it is closed; the boot-time listener refuses it.
func TestSealedWindowRefusal_ClusterUnsealIsNeverAnonymous(t *testing.T) {
	probe := &fakeSealedProbe{sealed: true, enrollment: true}
	regular := newTestServer(&mockKVService{}, &mockAdminService{}, WithSealedWindowProbe(probe))
	listener := newTestServer(&mockKVService{}, &mockAdminService{}, WithSealedWindowListener(probe))
	if _, refused := sealedWindowGateRefusal(context.Background(), "barrier.ClusterUnseal", regular.sealedWindowProbe, regular.sealedWindowListener); refused {
		t.Error("regular gate refused barrier.ClusterUnseal")
	}
	if r, refused := sealedWindowGateRefusal(context.Background(), "barrier.ClusterUnseal", listener.sealedWindowProbe, listener.sealedWindowListener); !refused || r.status != wire.StatusUnavailable {
		t.Errorf("boot-time listener: barrier.ClusterUnseal = (%+v, %v), want refused Unavailable", r, refused)
	}
}
