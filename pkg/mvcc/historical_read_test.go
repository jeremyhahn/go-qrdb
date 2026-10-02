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

package mvcc

import (
	"errors"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// TestRange_AtARevisionListsKeysDeletedSince: a Range at revision R returns
// every key live at R -- including keys deleted after R, whose current
// metadata is gone. The read enumerated the CURRENT keys and so silently
// omitted them.
func TestRange_AtARevisionListsKeysDeletedSince(t *testing.T) {
	s := newTestStore(t)
	for _, k := range []string{"r/a", "r/b", "r/c"} {
		if _, err := s.Put([]byte(k), []byte(k+"-v"), 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	at := s.CurrentRevision()
	if _, err := s.Delete([]byte("r/b")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Put([]byte("r/d"), []byte("r/d-v"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}

	kvs, _, _, err := s.Range([]byte("r/"), []byte("r0"), service.WithRevision(at))
	if err != nil {
		t.Fatalf("Range at %d: %v", at, err)
	}
	var keys []string
	for _, kv := range kvs {
		keys = append(keys, string(kv.Key)+"="+string(kv.Value))
	}
	sort.Strings(keys)
	if want := []string{"r/a=r/a-v", "r/b=r/b-v", "r/c=r/c-v"}; len(keys) != 3 || keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] {
		t.Fatalf("Range at %d = %v, want %v (r/b was live then; r/d was not)", at, keys, want)
	}

	_, count, _, err := s.Range([]byte("r/"), []byte("r0"), service.WithRevision(at), service.WithCountOnly())
	if err != nil || count != 3 {
		t.Fatalf("CountOnly Range at %d = %d, %v; want 3", at, count, err)
	}
	if kvs, _, _, err := s.Range([]byte("r/b"), nil, service.WithRevision(at)); err != nil || len(kvs) != 1 {
		t.Fatalf("single-key Range of r/b at %d = %v, %v", at, kvs, err)
	}
	if kvs, _, _, err := s.Range([]byte("r/"), []byte("r0")); err != nil || len(kvs) != 3 {
		t.Fatalf("current Range = %v, %v; want r/a, r/c, r/d", kvs, err)
	}
}

// compactingStorage runs onRead once, on the first read of a data entry --
// that is, after a reader checked the compaction floor and before it read --
// so a test can land a compaction exactly in that window.
type compactingStorage struct {
	storage.Storage
	onRead func()
	armed  bool
}

func (c *compactingStorage) fire() {
	if c.armed {
		c.armed = false
		c.onRead()
	}
}

func (c *compactingStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if opts != nil && len(opts.LowerBound) > 0 && opts.LowerBound[0] == prefixData {
		c.fire()
	}
	return c.Storage.NewIterator(opts)
}

// TestReads_ACompactionBetweenTheFloorCheckAndTheReadIsReported: a read at a
// revision checks the floor, then reads. A compaction that commits between
// the two used to make the read come back as absent -- a wrong answer for a
// revision that the floor now refuses. The read re-checks the floor after it
// reads and answers RevisionCompactedError instead.
func TestReads_ACompactionBetweenTheFloorCheckAndTheReadIsReported(t *testing.T) {
	reads := map[string]func(s *Store, rev int64) error{
		"GetAtRevision": func(s *Store, rev int64) error {
			_, err := s.GetAtRevision([]byte("acme/app/k"), rev)
			return err
		},
		"Range": func(s *Store, rev int64) error {
			_, _, _, err := s.Range([]byte("acme/app/"), []byte("acme/app0"), service.WithRevision(rev))
			return err
		},
	}
	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			stg := &compactingStorage{Storage: memory.NewStorage()}
			t.Cleanup(func() { _ = stg.Close() })
			s, err := New(stg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			for _, v := range []string{"v1", "v2", "v3"} {
				if _, err := s.Put([]byte("acme/app/k"), []byte(v), 0); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
			stg.onRead = func() {
				if err := s.CompactPrefix([]byte("acme/app/"), 2); err != nil {
					t.Fatalf("CompactPrefix: %v", err)
				}
			}
			stg.armed = true

			err = read(s, 1)
			var compacted *RevisionCompactedError
			if !errors.As(err, &compacted) {
				t.Fatalf("a read at 1 racing a compaction to 2 = %v, want *RevisionCompactedError", err)
			}
		})
	}
}
