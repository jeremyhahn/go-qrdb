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

package storage_test

import (
	"bytes"
	"testing"
)

// These cells extend TestBinaryKeyParity (every single byte, ordering, and
// the NUL-prefixed table under scans and List) to the key shapes and code
// paths it does not reach: keys that read as filesystem paths, a key long
// enough that objstore names it by hash, and binary keys going through a
// batch and a snapshot rather than a direct Set.

// shapedKeys are keys a storage engine that maps keys onto names could
// mistake for structure: path separators, traversal, a drive letter, control
// characters and an embedded NUL.
func shapedKeys() map[string][]byte {
	long := make([]byte, 4096)
	for i := range long {
		long[i] = byte(i)
	}
	return map[string][]byte{
		"nul-prefixed-table-key": append([]byte("\x00qrdb.lease/b/"), 0, 0, 0, 0, 0, 0, 0, 1),
		"ff-sentinel":            {0xff, 0xff, 0xff, 0xff},
		"single-slash":           []byte("a/b"),
		"double-slash":           []byte("a//b"),
		"trailing-slash":         []byte("a/"),
		"dot-dot-traversal":      []byte("../etc/passwd"),
		"leading-slash":          []byte("/etc/passwd"),
		"windows-drive":          []byte(`C:\evil`),
		"control-chars":          []byte("a\nb\rc\td"),
		"embedded-nul":           []byte("tenant\x00escape"),
		"iam-claim-separator":    []byte("acme/__sys:identity_idx/tenant/user/42"),
		"long-mixed-content":     long,
	}
}

func TestBinaryKeyParity_ShapedKeysRoundTrip(t *testing.T) {
	for _, b := range newParityBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			for name, k := range shapedKeys() {
				t.Run(name, func(t *testing.T) {
					val := []byte("value-for-" + name)
					if err := b.s.Set(k, val); err != nil {
						t.Fatalf("set %x: %v", k, err)
					}
					got, err := b.s.Get(k)
					if err != nil {
						t.Fatalf("get %x: %v", k, err)
					}
					if !bytes.Equal(got, val) {
						t.Fatalf("get %x = %q, want %q", k, got, val)
					}
					listed, err := b.s.List(k)
					if err != nil {
						t.Fatalf("list %x: %v", k, err)
					}
					if len(listed) != 1 || !bytes.Equal(listed[0], k) {
						t.Fatalf("list %x = %x, want exactly the key", k, listed)
					}
					if err := b.s.Delete(k); err != nil {
						t.Fatalf("delete %x: %v", k, err)
					}
					if got, err = b.s.Get(k); err != nil || got != nil {
						t.Fatalf("get %x after delete = %q, %v; want nil, nil", k, got, err)
					}
				})
			}
		})
	}
}

func TestBinaryKeyParity_ShapedKeysDoNotAlias(t *testing.T) {
	keys := shapedKeys()
	for _, b := range newParityBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			for name, k := range keys {
				if err := b.s.Set(k, []byte(name)); err != nil {
					t.Fatalf("set %s: %v", name, err)
				}
			}
			for name, k := range keys {
				got, err := b.s.Get(k)
				if err != nil {
					t.Fatalf("get %s: %v", name, err)
				}
				if string(got) != name {
					t.Fatalf("get %s = %q: another key's write landed on it", name, got)
				}
			}
			all, err := b.s.List(nil)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(all) != len(keys) {
				t.Fatalf("list yielded %d keys, want %d", len(all), len(keys))
			}
		})
	}
}

func TestBinaryKeyParity_Batch(t *testing.T) {
	nulKey := []byte{0x00, 'a'}
	ffKey := []byte{0xff, 'b'}
	for _, b := range newParityBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			if err := b.s.Set(nulKey, []byte("pre")); err != nil {
				t.Fatalf("seed set: %v", err)
			}

			batch := b.s.Batch()
			defer func() { _ = batch.Close() }()
			if err := batch.Set(ffKey, []byte("new")); err != nil {
				t.Fatalf("batch set %x: %v", ffKey, err)
			}
			if err := batch.Delete(nulKey); err != nil {
				t.Fatalf("batch delete %x: %v", nulKey, err)
			}
			if err := batch.Commit(); err != nil {
				t.Fatalf("batch commit: %v", err)
			}

			got, err := b.s.Get(ffKey)
			if err != nil {
				t.Fatalf("get %x: %v", ffKey, err)
			}
			if !bytes.Equal(got, []byte("new")) {
				t.Fatalf("get %x = %q, want %q", ffKey, got, "new")
			}
			if got, err = b.s.Get(nulKey); err != nil || got != nil {
				t.Fatalf("get %x after batch delete = %q, %v; want nil, nil", nulKey, got, err)
			}
		})
	}
}

func TestBinaryKeyParity_BatchRollback(t *testing.T) {
	k := []byte{0x00, 0xff, '/', 0x00}
	for _, b := range newParityBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			batch := b.s.Batch()
			if err := batch.Set(k, []byte("v")); err != nil {
				t.Fatalf("batch set %x: %v", k, err)
			}
			if err := batch.Rollback(); err != nil {
				t.Fatalf("batch rollback: %v", err)
			}
			_ = batch.Close()
			if got, err := b.s.Get(k); err != nil || got != nil {
				t.Fatalf("get %x after rolled-back batch = %q, %v; want nil, nil", k, got, err)
			}
		})
	}
}

func TestBinaryKeyParity_SnapshotIsolation(t *testing.T) {
	k := []byte{0x00, 0xff, 0x00}
	for _, b := range newParityBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			if err := b.s.Set(k, []byte("v1")); err != nil {
				t.Fatalf("set: %v", err)
			}
			snap, err := b.s.Snapshot()
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			defer func() { _ = snap.Close() }()

			if err := b.s.Set(k, []byte("v2")); err != nil {
				t.Fatalf("set after snapshot: %v", err)
			}

			sv, err := snap.Get(k)
			if err != nil {
				t.Fatalf("snapshot get %x: %v", k, err)
			}
			if !bytes.Equal(sv, []byte("v1")) {
				t.Fatalf("snapshot get %x = %q, want %q", k, sv, "v1")
			}
			live, err := b.s.Get(k)
			if err != nil {
				t.Fatalf("live get %x: %v", k, err)
			}
			if !bytes.Equal(live, []byte("v2")) {
				t.Fatalf("live get %x = %q, want %q", k, live, "v2")
			}
		})
	}
}

func TestBinaryKeyParity_SnapshotHidesLaterBinaryKey(t *testing.T) {
	k := []byte{0xff, 0x00, 0xff}
	for _, b := range newParityBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			snap, err := b.s.Snapshot()
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			defer func() { _ = snap.Close() }()

			if err := b.s.Set(k, []byte("later")); err != nil {
				t.Fatalf("set after snapshot: %v", err)
			}
			if sv, err := snap.Get(k); err != nil || sv != nil {
				t.Fatalf("snapshot get %x = %q, %v; want nil, nil", k, sv, err)
			}
		})
	}
}
