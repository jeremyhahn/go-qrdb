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

import "testing"

func TestSealedWindowAllowed_AllowlistedMethods(t *testing.T) {
	for _, method := range []string{
		"admin.Health", "admin.Status",
		"barrier.Status", "barrier.Initialize", "barrier.Unseal",
		"barrier.ShamirInitialize", "barrier.Rekey",
		"ceremony.Initialize", "ceremony.Status",
	} {
		if !sealedWindowAllowed(method) {
			t.Errorf("expected %q to be allowed while sealed", method)
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
type fakeSealedProbe struct{ sealed bool }

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
