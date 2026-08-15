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

// Message tests for the tenant errors an operator sees when a CreateTenant
// option or a cross-tenant request is refused. Each one is a refusal the
// operator has to act on, so the message must carry the offending value rather
// than only the category.

package tenant

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRefusalMessagesCarryTheOffendingValue covers every rejection error's
// message. These are the sole diagnostic for a refused tenant creation or a
// denied cross-tenant call: the operation is rejected before anything is
// written, so there is no state left to inspect afterwards.
func TestRefusalMessagesCarryTheOffendingValue(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		contains []string
	}{
		{
			name: "cross-tenant names both tenants and the operation",
			err: &CrossTenantDeniedError{
				IdentityTenantID:  "acme",
				RequestedTenantID: "globex",
				Op:                "create user",
			},
			contains: []string{"tenant:", "acme", "globex", "create user"},
		},
		{
			name: "invalid data root names the path and the reason",
			err: &InvalidDataRootError{
				DataRoot: "relative/path",
				Reason:   "must be absolute",
			},
			contains: []string{"invalid data root", "relative/path", "must be absolute"},
		},
		{
			name:     "non-empty data root names the path",
			err:      &DataRootNotEmptyError{DataRoot: "/mnt/disk1/tenant-a"},
			contains: []string{"/mnt/disk1/tenant-a", "not empty"},
		},
		{
			name: "overlap names both paths and the kind",
			err: &DataRootOverlapError{
				DataRoot: "/mnt/pool/tenant-a",
				Conflict: "/mnt/pool",
				Kind:     "volume pool",
			},
			contains: []string{"/mnt/pool/tenant-a", "overlaps", "volume pool", "/mnt/pool"},
		},
		{
			name: "isolation-type mismatch names the tenant and the type",
			err: &DataRootIsolationTypeError{
				TenantID:      "acme",
				IsolationType: IsolationPrefix,
			},
			contains: []string{"acme", string(IsolationPrefix), "volume or raft"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("message %q does not contain %q", got, want)
				}
			}
		})
	}
}

// TestIsPathAncestorDistinguishesSiblingsFromChildren is the check that keeps
// one tenant's data root from being accepted inside another's.
//
// The sibling case is the one that matters and the one a naive
// strings.HasPrefix gets wrong: /data/foo2 shares a textual prefix with
// /data/foo but is a completely separate directory. Accepting it as a child
// would reject a legitimate data root; treating a real child as a sibling
// would let two tenants share a disk location.
func TestIsPathAncestorDistinguishesSiblingsFromChildren(t *testing.T) {
	tests := []struct {
		name   string
		parent string
		child  string
		want   bool
	}{
		{"direct child", "/data/foo", "/data/foo/bar", true},
		{"deep descendant", "/data/foo", "/data/foo/bar/baz/qux", true},
		{"identical paths are not ancestry", "/data/foo", "/data/foo", false},
		{"textual-prefix sibling", "/data/foo", "/data/foo2", false},
		{"parent is below the child", "/data/foo/bar", "/data/foo", false},
		{"unrelated trees", "/data/foo", "/srv/other", false},
		{"root is an ancestor of everything", "/", "/data/foo", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parent := filepath.Clean(tc.parent)
			child := filepath.Clean(tc.child)
			if got := isPathAncestor(parent, child); got != tc.want {
				t.Fatalf("isPathAncestor(%q, %q) = %v, want %v", parent, child, got, tc.want)
			}
		})
	}
}

// TestIsPathAncestorIsNotSymmetric pins the direction explicitly: ancestry is
// a one-way relation, and a check that accepted either direction would let a
// tenant claim a path that contains an existing one.
func TestIsPathAncestorIsNotSymmetric(t *testing.T) {
	const parent, child = "/data/foo", "/data/foo/bar"

	if !isPathAncestor(parent, child) {
		t.Fatal("parent must be an ancestor of child")
	}
	if isPathAncestor(child, parent) {
		t.Fatal("child must not be an ancestor of parent")
	}
}
