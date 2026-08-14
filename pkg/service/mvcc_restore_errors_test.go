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

package service

import (
	"errors"
	"strings"
	"testing"
)

// TestRestoreErrors_MessagesIdentifyTheRefusal checks every restore error
// renders a non-empty, package-prefixed message naming what was refused.
// These strings reach operators through every transport, and a refusal an
// operator cannot interpret gets worked around rather than understood.
func TestRestoreErrors_MessagesIdentifyTheRefusal(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		contains []string
	}{
		{"nil request", &RestoreNilRequestError{}, []string{"nil"}},
		{"invalid revision", &RestoreRevisionInvalidError{Revision: -3}, []string{"-3", "greater than zero"}},
		{"future revision", &RestoreRevisionFutureError{Requested: 90, Current: 12}, []string{"90", "12"}},
		{"empty scope", &RestoreScopeEmptyError{}, []string{"exactly one", "whole keyspace"}},
		{"ambiguous scope", &RestoreScopeAmbiguousError{Prefix: "app/", KeyCount: 2}, []string{"app/", "2"}},
		{"empty key", &RestoreEmptyKeyError{Index: 4}, []string{"4"}},
		{"internal key", &RestoreInternalKeyError{Key: "__sys:roles/a"}, []string{"__sys:roles/a", "not restorable"}},
		{"internal prefix", &RestoreInternalKeyError{Prefix: "ts:"}, []string{"ts:", "not restorable"}},
		{"too large", &RestoreScopeTooLargeError{Limit: 10, Scope: "prefix app/"}, []string{"10", "app/"}},
		{"unbounded prefix", &RestoreUnboundedPrefixError{Prefix: "\xff"}, []string{"no finite upper bound"}},
		{"no change index", &RestoreChangeIndexUnavailableError{
			Err: errors.New("backend has no revision index"),
		}, []string{"revision index", "name the keys explicitly"}},
		{"apply failure", &RestoreApplyError{
			Key: "app/a", Action: RestoreActionRevert, Applied: 3, Err: errors.New("raft down"),
		}, []string{"app/a", "revert", "3", "raft down"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			if !strings.HasPrefix(msg, "service: ") {
				t.Fatalf("message does not identify its origin: %q", msg)
			}
			for _, want := range tc.contains {
				if !strings.Contains(msg, want) {
					t.Fatalf("message %q does not mention %q", msg, want)
				}
			}
		})
	}
}

// TestRestoreInternalKeyError_NamesPrefixWhenNoKeyIsSet checks the message
// switches between the two scope shapes rather than rendering an empty target.
func TestRestoreInternalKeyError_NamesPrefixWhenNoKeyIsSet(t *testing.T) {
	byKey := (&RestoreInternalKeyError{Key: "ts:cpu"}).Error()
	if !strings.Contains(byKey, "key ts:cpu") {
		t.Fatalf("key form: %q", byKey)
	}
	byPrefix := (&RestoreInternalKeyError{Prefix: "ts:"}).Error()
	if !strings.Contains(byPrefix, "prefix ts:") {
		t.Fatalf("prefix form: %q", byPrefix)
	}
}

func TestRestoreApplyError_UnwrapsTheUnderlyingWriteError(t *testing.T) {
	inner := errors.New("propose rejected")
	err := error(&RestoreApplyError{Key: "app/a", Action: RestoreActionDelete, Err: inner})

	if !errors.Is(err, inner) {
		t.Fatal("errors.Is did not reach the underlying write error")
	}
	var applyErr *RestoreApplyError
	if !errors.As(err, &applyErr) || applyErr.Key != "app/a" {
		t.Fatalf("errors.As did not recover the apply error: %#v", err)
	}
}

func TestUnknownRestoreActionError_NamesTheAction(t *testing.T) {
	err := &unknownRestoreActionError{Action: RestoreAction("teleport")}
	if !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("message does not name the action: %q", err.Error())
	}
}

func TestRestoreChangeIndexUnavailableError_UnwrapsBackendRefusal(t *testing.T) {
	inner := errors.New("no revision index")
	err := error(&RestoreChangeIndexUnavailableError{Err: inner})

	if !errors.Is(err, inner) {
		t.Fatal("errors.Is did not reach the backend refusal")
	}
	var unavailable *RestoreChangeIndexUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("errors.As did not recover the typed error: %#v", err)
	}
}
