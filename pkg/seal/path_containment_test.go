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

package seal

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestContainedTenantPath_ValidIDs verifies ordinary tenant ids resolve to a
// strict child of the pool root.
func TestContainedTenantPath_ValidIDs(t *testing.T) {
	root := filepath.Join("/", "var", "lib", "qrdb", "pool0")
	for _, id := range []string{"acme", "tenant-1", "user_tenant", "abc.123"} {
		t.Run(id, func(t *testing.T) {
			got, err := containedTenantPath(root, id)
			if err != nil {
				t.Fatalf("containedTenantPath(%q, %q): %v", root, id, err)
			}
			want := filepath.Join(root, id)
			if got != want {
				t.Errorf("path = %q, want %q", got, want)
			}
		})
	}
}

// TestContainedTenantPath_TraversalIDs verifies path-traversal tenant ids are
// rejected with ErrTenantPathEscapesPool — the fail-closed defense at the
// filesystem sink, independent of the tenant-service id validation.
func TestContainedTenantPath_TraversalIDs(t *testing.T) {
	root := filepath.Join("/", "var", "lib", "qrdb", "pool0")
	cases := []struct {
		name string
		id   string
	}{
		{"dotdot escape", "../../etc"},
		{"single dotdot", ".."},
		{"dot", "."},
		{"empty", ""},
		{"slash infix nests deeper", "acme/prod"},
		{"absolute path", "/etc/passwd"},
		{"dotdot then descend", "../pool1/victim"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := containedTenantPath(root, tc.id); !errors.Is(err, ErrTenantPathEscapesPool) {
				t.Fatalf("containedTenantPath(%q, %q): expected ErrTenantPathEscapesPool, got %v", root, tc.id, err)
			}
		})
	}
}
