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

package pool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/validation"
)

// BuildLocalCandidates creates caller-supplied directories. #292 filed it as a
// sink to guard BEFORE a transport makes it reachable: RegisterVolumePoolRequest
// is already wire-shaped, and gosec cannot see it because G304 covers file
// opens, not directory creation.
//
// Every case asserts the typed error, not just failure, because "it returned
// an error" is also what a bad path separator or a full disk looks like.
func TestBuildLocalCandidatesRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()

	// A symlinked ancestor: canonical by inspection, elsewhere in fact. This
	// is the case filepath.Clean cannot catch.
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	linkDir := filepath.Join(root, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"empty", "", "must not be empty"},
		{"relative", "data/pool", "must be an absolute path"},
		// Built by concatenation, NOT filepath.Join: Join cleans its result, so
		// it cannot produce the non-canonical path this case is about.
		{"dot-dot", root + "/a/../b", "must be a canonical path"},
		{"trailing separator", root + "/pool/", "must be a canonical path"},
		{"symlinked ancestor", filepath.Join(linkDir, "vol"), "traverses a symlink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildLocalCandidates("p", []string{tc.path})
			if err == nil {
				t.Fatalf("accepted %q", tc.path)
			}
			var pathErr *VolumePoolPathError
			if !errors.As(err, &pathErr) {
				t.Fatalf("got %T (%v), want *VolumePoolPathError", err, err)
			}
			var vErr *validation.PathError
			if !errors.As(err, &vErr) {
				t.Fatalf("got %v, want a wrapped *validation.PathError", err)
			}
			if !strings.Contains(vErr.Reason, tc.want) {
				t.Errorf("reason %q does not mention %q", vErr.Reason, tc.want)
			}
		})
	}
}

// A pool whose volumes contain one another is one volume counted twice, and a
// placement strategy would report free space that does not exist.
func TestBuildLocalCandidatesRejectsOverlappingPaths(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "vol")
	inner := filepath.Join(outer, "nested")

	_, err := BuildLocalCandidates("p", []string{outer, inner})
	if err == nil {
		t.Fatal("accepted a pool whose second volume sits inside its first")
	}
	var overlap *VolumePoolOverlapError
	if !errors.As(err, &overlap) {
		t.Fatalf("got %T (%v), want *VolumePoolOverlapError", err, err)
	}
	if overlap.Other != outer {
		t.Errorf("Other = %q, want %q", overlap.Other, outer)
	}
}

// The separator-aware half of the overlap rule: /data/foo2 is NOT inside
// /data/foo, and a strings.HasPrefix implementation would say it is.
func TestBuildLocalCandidatesAcceptsSiblingsSharingAPrefix(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "foo")
	b := filepath.Join(root, "foo2")

	got, err := BuildLocalCandidates("p", []string{a, b})
	if err != nil {
		t.Fatalf("rejected sibling volumes sharing a name prefix: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	for _, d := range []string{a, b} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("volume %q was not created: %v", d, err)
		}
	}
}

// The accepting branch: an ordinary absolute path is created 0700 and returned.
func TestBuildLocalCandidatesCreatesVolumeRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vol")

	got, err := BuildLocalCandidates("p", []string{dir})
	if err != nil {
		t.Fatalf("BuildLocalCandidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("volume root not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("volume root mode %04o, want 0700", perm)
	}
}
