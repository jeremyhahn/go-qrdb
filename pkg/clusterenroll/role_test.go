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

package clusterenroll

import (
	"strings"
	"testing"
)

// TestUnknownRoleError_ErrorString exercises UnknownRoleError.Error(), which
// persistRole/loadRole's own round-trip tests never reach because they never
// write a role file this package does not recognise.
func TestUnknownRoleError_ErrorString(t *testing.T) {
	err := &UnknownRoleError{Value: "bogus-role"}
	got := err.Error()
	if got == "" {
		t.Fatal("UnknownRoleError.Error() is empty")
	}
	want := "clusterenroll: unrecognised enrollment role bogus-role in " + RoleFile +
		"; expected first-node, single-node or joined"
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestIncompleteFirstNodeMaterialError_ErrorString exercises
// IncompleteFirstNodeMaterialError.Error(), constructed directly rather than
// through a missing-material fixture: the message format is what is under
// test, not the detection path (already covered by resume_advertiser_test.go).
func TestIncompleteFirstNodeMaterialError_ErrorString(t *testing.T) {
	err := &IncompleteFirstNodeMaterialError{Dir: "/var/lib/qrdb", Missing: []string{"ca-key.pem", "noise-key.hex"}}
	got := err.Error()
	if got == "" {
		t.Fatal("IncompleteFirstNodeMaterialError.Error() is empty")
	}
	for _, want := range []string{"/var/lib/qrdb", "ca-key.pem", "noise-key.hex"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Error() = %q, want it to contain %q", got, want)
		}
	}
}
