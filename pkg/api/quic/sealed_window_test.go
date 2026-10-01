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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

func TestSealedWindowAllowed_AllowlistedMethods(t *testing.T) {
	for _, method := range []string{
		"admin.Health", "admin.Status",
		"barrier.Status", "barrier.Initialize", "barrier.Unseal",
		"barrier.ShamirInitialize", "barrier.RootToken",
		"ceremony.Initialize", "ceremony.Status",
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

	if _, refused := none.sealedWindowRefusal("kv.Put"); refused {
		t.Error("no gate installed: kv.Put refused")
	}
	for _, srv := range []*Server{regular, listener} {
		if _, refused := srv.sealedWindowRefusal("barrier.Unseal"); refused {
			t.Error("sealed: barrier.Unseal refused")
		}
		if r, refused := srv.sealedWindowRefusal("kv.Put"); !refused || r.msg != "barrier sealed" || r.status != wire.StatusUnavailable {
			t.Errorf("sealed: kv.Put = (%+v, %v), want (Unavailable barrier sealed, true)", r, refused)
		}
	}

	// Officer registration: the listener refuses it once enrollment has
	// closed; the regular gate leaves it to authentication.
	if r, refused := listener.sealedWindowRefusal("barrier.OfficerRegister"); !refused || r.status != wire.StatusPermissionDenied || r.msg != sealedwindow.EnrollmentClosedMessage {
		t.Errorf("listener, enrollment closed: barrier.OfficerRegister = (%+v, %v), want PermissionDenied", r, refused)
	}
	if _, refused := regular.sealedWindowRefusal("barrier.OfficerRegister"); refused {
		t.Error("regular gate: barrier.OfficerRegister refused")
	}
	probe.enrollment = true
	if _, refused := listener.sealedWindowRefusal("barrier.RecoveryOfficerRegister"); refused {
		t.Error("listener, enrollment open: barrier.RecoveryOfficerRegister refused")
	}

	probe.sealed = false
	if _, refused := regular.sealedWindowRefusal("kv.Put"); refused {
		t.Error("regular gate, unsealed: kv.Put refused")
	}
	for _, method := range []string{"barrier.Unseal", "kv.Put"} {
		r, refused := listener.sealedWindowRefusal(method)
		if !refused || r.msg != sealedwindow.ClosedMessage {
			t.Errorf("listener gate, unsealed: %s = (%+v, %v), want (%q, true)", method, r, refused, sealedwindow.ClosedMessage)
		}
	}
}

func TestSealedWindowAllowed_NonAllowlistedMethods(t *testing.T) {
	for _, method := range []string{
		"kv.Put", "kv.Get", "db.Create", "sql.Query", "user.Create",
	} {
		if sealedWindowAllowed(method) {
			t.Errorf("expected %q to be rejected while sealed", method)
		}
	}
}

// fakeSealedProbe is a deterministic sealedWindowProbe test double.
type fakeSealedProbe struct {
	sealed, enrollment bool
	sealedwindow.Admission
}

func (f *fakeSealedProbe) EnrollmentOpen() bool { return f.enrollment }

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
