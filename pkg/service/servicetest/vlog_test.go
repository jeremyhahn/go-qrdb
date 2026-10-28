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

//go:build qrdb_vlog

package servicetest

import (
	"testing"

	"github.com/transparency-dev/merkle/rfc6962"
)

// threeLeafTree is the RFC 9162 tree over the bodies a, b and c, built by
// hand so the verifiers are checked against the RFC, not against the engine
// they exist to check.
type threeLeafTree struct {
	leaves [3][]byte
	root1  []byte
	root3  []byte
}

func newThreeLeafTree() threeLeafTree {
	h := rfc6962.DefaultHasher
	var tr threeLeafTree
	for i, body := range []string{"a", "b", "c"} {
		tr.leaves[i] = h.HashLeaf([]byte(body))
	}
	tr.root1 = tr.leaves[0]
	tr.root3 = h.HashChildren(h.HashChildren(tr.leaves[0], tr.leaves[1]), tr.leaves[2])
	return tr
}

func TestVerifyInclusion_AcceptsTheRFCPath(t *testing.T) {
	tr := newThreeLeafTree()
	if err := VerifyInclusion([]byte("b"), 1, 3, [][]byte{tr.leaves[0], tr.leaves[2]}, tr.root3); err != nil {
		t.Fatalf("VerifyInclusion of b at 1 in size 3: %v", err)
	}
}

func TestVerifyInclusion_RejectsAWrongBodyOrPath(t *testing.T) {
	tr := newThreeLeafTree()
	cases := map[string]struct {
		body   string
		index  uint64
		hashes [][]byte
	}{
		"wrong body":  {"x", 1, [][]byte{tr.leaves[0], tr.leaves[2]}},
		"wrong index": {"b", 0, [][]byte{tr.leaves[0], tr.leaves[2]}},
		"short path":  {"b", 1, [][]byte{tr.leaves[0]}},
	}
	for name, c := range cases {
		if err := VerifyInclusion([]byte(c.body), c.index, 3, c.hashes, tr.root3); err == nil {
			t.Errorf("%s: VerifyInclusion accepted it", name)
		}
	}
}

func TestVerifyConsistency_AcceptsTheRFCPath(t *testing.T) {
	tr := newThreeLeafTree()
	if err := VerifyConsistency(1, 3, [][]byte{tr.leaves[1], tr.leaves[2]}, tr.root1, tr.root3); err != nil {
		t.Fatalf("VerifyConsistency 1 -> 3: %v", err)
	}
}

func TestVerifyConsistency_RejectsAWrongRootOrPath(t *testing.T) {
	tr := newThreeLeafTree()
	cases := map[string]struct {
		hashes [][]byte
		root1  []byte
	}{
		"wrong old root": {[][]byte{tr.leaves[1], tr.leaves[2]}, tr.leaves[1]},
		"swapped path":   {[][]byte{tr.leaves[2], tr.leaves[1]}, tr.root1},
		"short path":     {[][]byte{tr.leaves[1]}, tr.root1},
	}
	for name, c := range cases {
		if err := VerifyConsistency(1, 3, c.hashes, c.root1, tr.root3); err == nil {
			t.Errorf("%s: VerifyConsistency accepted it", name)
		}
	}
}
