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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Tenant / database confinement of a selective restore
//
// The namespaced wrapper is where a restore is bound to one tenant and
// database. These tests pin the two properties that make the binding
// unbypassable: every key the request reaches is derived from the namespace
// prefix, and the guards that prefixing would defeat are evaluated on the
// caller's own request first.
// ---------------------------------------------------------------------------

func TestNamespacedMVCC_RestoreToRevision_PrefixesScopeAndStripsPlan(t *testing.T) {
	rec := newMVCCRec()
	w := newNamespacedMVCCService(rec, "acme/reports/", nil)

	result, err := w.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 7,
		Prefix:   "daily/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}

	if rec.restoreReq.Prefix != "acme/reports/daily/" {
		t.Fatalf("inner prefix = %q, want acme/reports/daily/", rec.restoreReq.Prefix)
	}
	if len(result.Changes) != 1 || result.Changes[0].Key != "daily/x" {
		t.Fatalf("plan keys not stripped to the caller's view: %+v", result.Changes)
	}
}

func TestNamespacedMVCC_RestoreToRevision_PrefixesEveryExplicitKey(t *testing.T) {
	rec := newMVCCRec()
	w := newNamespacedMVCCService(rec, "acme/reports/", nil)

	result, err := w.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 7,
		Keys:     []string{"a", "b/c"},
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}

	want := []string{"acme/reports/a", "acme/reports/b/c"}
	if len(rec.restoreReq.Keys) != len(want) {
		t.Fatalf("inner keys = %v, want %v", rec.restoreReq.Keys, want)
	}
	for i := range want {
		if rec.restoreReq.Keys[i] != want[i] {
			t.Fatalf("inner keys = %v, want %v", rec.restoreReq.Keys, want)
		}
	}
	for i, expected := range []string{"a", "b/c"} {
		if result.Changes[i].Key != expected {
			t.Fatalf("change %d key = %q, want %q", i, result.Changes[i].Key, expected)
		}
	}
}

// TestNamespacedMVCC_RestoreToRevision_DoesNotMutateCallerRequest proves the
// wrapper builds a new scoped request rather than rewriting the caller's, so a
// caller that reuses a request does not accumulate namespace prefixes.
func TestNamespacedMVCC_RestoreToRevision_DoesNotMutateCallerRequest(t *testing.T) {
	rec := newMVCCRec()
	w := newNamespacedMVCCService(rec, "acme/reports/", nil)

	req := &service.RestoreRevisionRequest{Revision: 7, Keys: []string{"a"}}
	if _, err := w.RestoreToRevision(context.Background(), req); err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if req.Keys[0] != "a" {
		t.Fatalf("caller request was mutated: %v", req.Keys)
	}

	if _, err := w.RestoreToRevision(context.Background(), req); err != nil {
		t.Fatalf("second RestoreToRevision: %v", err)
	}
	if rec.restoreReq.Keys[0] != "acme/reports/a" {
		t.Fatalf("prefix accumulated across calls: %q", rec.restoreReq.Keys[0])
	}
}

// TestNamespacedMVCC_RestoreToRevision_RejectsEmptyScopeBeforePrefixing is the
// reason validation precedes prefixing. "acme/reports/" + "" is a non-empty
// prefix the inner service would accept, and it spans this tenant and
// database's entire keyspace — a whole-database rollback reached by supplying
// no scope at all.
func TestNamespacedMVCC_RestoreToRevision_RejectsEmptyScopeBeforePrefixing(t *testing.T) {
	rec := newMVCCRec()
	w := newNamespacedMVCCService(rec, "acme/reports/", nil)

	_, err := w.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{Revision: 7})
	var empty *service.RestoreScopeEmptyError
	if !errors.As(err, &empty) {
		t.Fatalf("expected *service.RestoreScopeEmptyError, got %#v", err)
	}
	if rec.restoreReq != nil {
		t.Fatalf("an unscoped request reached the inner service: %+v", rec.restoreReq)
	}
}

// TestNamespacedMVCC_RestoreToRevision_RejectsInternalScopeBeforePrefixing is
// the other guard prefixing would defeat: once "__sys:roles/x" has become
// "acme/reports/__sys:roles/x" it is an ordinary user key, and the inner
// service's internal-key guard admits it.
func TestNamespacedMVCC_RestoreToRevision_RejectsInternalScopeBeforePrefixing(t *testing.T) {
	cases := []struct {
		name string
		req  *service.RestoreRevisionRequest
	}{
		{"system prefix", &service.RestoreRevisionRequest{Revision: 7, Prefix: "__sys:"}},
		{"system role prefix", &service.RestoreRevisionRequest{Revision: 7, Prefix: "__sys:roles/"}},
		{"system key", &service.RestoreRevisionRequest{Revision: 7, Keys: []string{"__sys:users/root"}}},
		{"timeseries key", &service.RestoreRevisionRequest{Revision: 7, Keys: []string{"ts:cpu/1"}}},
		{"metric index key", &service.RestoreRevisionRequest{Revision: 7, Keys: []string{"idx:metric:cpu"}}},
		{"tag index key", &service.RestoreRevisionRequest{Revision: 7, Keys: []string{"idx:tag:host"}}},
		{"retention key", &service.RestoreRevisionRequest{Revision: 7, Keys: []string{"retention:cpu"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newMVCCRec()
			w := newNamespacedMVCCService(rec, "acme/reports/", nil)

			_, err := w.RestoreToRevision(context.Background(), tc.req)
			var internal *service.RestoreInternalKeyError
			if !errors.As(err, &internal) {
				t.Fatalf("expected *service.RestoreInternalKeyError, got %#v", err)
			}
			if rec.restoreReq != nil {
				t.Fatalf("an internal-namespace request reached the inner service: %+v", rec.restoreReq)
			}
		})
	}
}

func TestNamespacedMVCC_RestoreToRevision_PropagatesInnerError(t *testing.T) {
	rec := newMVCCRec()
	rec.err = errBoom
	w := newNamespacedMVCCService(rec, "acme/reports/", nil)

	_, err := w.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 7, Prefix: "daily/",
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("expected the inner error, got %#v", err)
	}
}

func TestStripRestoreResult_HandlesNil(t *testing.T) {
	if got := stripRestoreResult(nil, "acme/"); got != nil {
		t.Fatalf("stripRestoreResult(nil) = %v, want nil", got)
	}
}

// TestNamespacedMVCC_RestoreToRevision_ForwardsForce checks the override
// reaches the inner service. A Force flag silently dropped by a wrapper would
// turn an operator's deliberate overwrite into a silent no-op on every
// conflicting key.
func TestNamespacedMVCC_RestoreToRevision_ForwardsForce(t *testing.T) {
	for _, force := range []bool{true, false} {
		rec := newMVCCRec()
		w := newNamespacedMVCCService(rec, "acme/reports/", nil)

		if _, err := w.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
			Revision: 7, Prefix: "daily/", Force: force,
		}); err != nil {
			t.Fatalf("force=%v: %v", force, err)
		}
		if rec.restoreReq.Force != force {
			t.Fatalf("force=%v was not forwarded (inner saw %v)", force, rec.restoreReq.Force)
		}
	}
}

// TestNamespacedMVCC_RestoreToRevision_StripsConflictKeys checks conflicts are
// namespaced back to the caller's view like the applied changes are. An
// operator shown a conflict on "acme/reports/daily/x" could not re-issue a
// restore for it: that key does not exist in their namespace.
func TestNamespacedMVCC_RestoreToRevision_StripsConflictKeys(t *testing.T) {
	rec := newMVCCRec()
	rec.conflictKeys = []string{"acme/reports/daily/conflicted"}
	w := newNamespacedMVCCService(rec, "acme/reports/", nil)

	result, err := w.RestoreToRevision(context.Background(), &service.RestoreRevisionRequest{
		Revision: 7, Prefix: "daily/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Key != "daily/conflicted" {
		t.Fatalf("conflicts not stripped to the caller's view: %+v", result.Conflicts)
	}
}
