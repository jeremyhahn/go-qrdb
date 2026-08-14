// Copyright (c) 2026 Jeremy Hahn
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

package permission

import "testing"

// sshVocabulary pins the literal wire strings of the SSH key vault family.
// The xkms transports (REST, gRPC, QUIC, MCP) gate their ssh.* surface on
// exactly these pairs, so a retyped constant here silently changes who may
// reach the SSH key store.
var sshVocabulary = []struct {
	perm     Permission
	resource string
	action   string
}{
	{SSHRead, "ssh", "read"},
	{SSHWrite, "ssh", "write"},
	{SSHDelete, "ssh", "delete"},
}

// TestSSHPermissions_Vocabulary asserts each SSH permission carries the exact
// wire string, splits into the expected resource and action, and is present in
// the global registry (so a role may actually grant it).
func TestSSHPermissions_Vocabulary(t *testing.T) {
	for _, tc := range sshVocabulary {
		want := tc.resource + ":" + tc.action
		if string(tc.perm) != want {
			t.Errorf("permission string: got %q, want %q", tc.perm, want)
		}
		if got := Resource(tc.perm); got != tc.resource {
			t.Errorf("Resource(%q): got %q, want %q", tc.perm, got, tc.resource)
		}
		if got := Action(tc.perm); got != tc.action {
			t.Errorf("Action(%q): got %q, want %q", tc.perm, got, tc.action)
		}
		if !IsRegistered(tc.perm) {
			t.Errorf("IsRegistered(%q): got false, want true", tc.perm)
		}
	}
}

// TestSSHPermissions_ByResource proves the family is reachable as a whole and
// that no permission outside the three declared verbs claims the ssh resource.
func TestSSHPermissions_ByResource(t *testing.T) {
	got := ByResource("ssh")
	if len(got) != len(sshVocabulary) {
		t.Fatalf("ByResource(\"ssh\"): got %d permissions %v, want %d", len(got), got, len(sshVocabulary))
	}

	declared := make(map[Permission]struct{}, len(sshVocabulary))
	for _, tc := range sshVocabulary {
		declared[tc.perm] = struct{}{}
	}
	for _, p := range got {
		if _, ok := declared[p]; !ok {
			t.Errorf("ByResource(\"ssh\") returned undeclared permission %q", p)
		}
	}
}

// TestSSHPermissions_UnknownActionNotRegistered is the negative case: an action
// that was never declared must not resolve, so a typo in a role grant is
// rejected by validatePermissions rather than silently accepted.
func TestSSHPermissions_UnknownActionNotRegistered(t *testing.T) {
	for _, p := range []Permission{"ssh:execute", "ssh:sign", "ssh:", "ssh"} {
		if IsRegistered(p) {
			t.Errorf("IsRegistered(%q): got true, want false", p)
		}
	}
}
