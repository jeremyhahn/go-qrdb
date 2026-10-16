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

package partition

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// seedRangeKeys writes k00..k19, which hash routing spreads over every
// partition of a four-partition manager.
func seedRangeKeys(t *testing.T, m *Manager) {
	t.Helper()
	for i := 0; i < 20; i++ {
		k := []byte(fmt.Sprintf("k%02d", i))
		if err := m.Set(k, k); err != nil {
			t.Fatal(err)
		}
	}
}

func managerKeys(t *testing.T, m *Manager) []string {
	t.Helper()
	keys, err := m.List(nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = string(k)
	}
	sort.Strings(out)
	return out
}

func keyRange(from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, fmt.Sprintf("k%02d", i))
	}
	return out
}

// rangeDeleters runs the same range delete through the manager and through a
// committed manager batch, so both paths are held to one contract.
var rangeDeleters = map[string]func(m *Manager, start, end []byte) error{
	"manager": func(m *Manager, start, end []byte) error { return m.DeleteRange(start, end) },
	"batch": func(m *Manager, start, end []byte) error {
		b := m.Batch()
		defer func() { _ = b.Close() }()
		if err := b.DeleteRange(start, end); err != nil {
			return err
		}
		return b.Commit()
	},
}

// TestDeleteRange_ReachesEveryPartition is the property the fan-out exists
// for: a contiguous key range is scattered by hash routing, so removing it
// from one partition would leave the rest of it behind.
func TestDeleteRange_ReachesEveryPartition(t *testing.T) {
	for name, del := range rangeDeleters {
		t.Run(name, func(t *testing.T) {
			m := newTestManager(t, 4)
			defer func() { _ = m.Close() }()
			seedRangeKeys(t, m)

			if err := del(m, []byte("k05"), []byte("k15")); err != nil {
				t.Fatalf("DeleteRange: %v", err)
			}
			want := append(keyRange(0, 5), keyRange(15, 20)...)
			if got := managerKeys(t, m); !reflect.DeepEqual(got, want) {
				t.Fatalf("keys = %v, want %v", got, want)
			}

			if err := del(m, []byte("k10"), nil); err != nil {
				t.Fatalf("unbounded DeleteRange: %v", err)
			}
			if got := managerKeys(t, m); !reflect.DeepEqual(got, keyRange(0, 5)) {
				t.Fatalf("keys after unbounded delete = %v, want %v", got, keyRange(0, 5))
			}
		})
	}
}

func TestDeleteRange_Refusals(t *testing.T) {
	m := newTestManager(t, 2)
	defer func() { _ = m.Close() }()
	seedRangeKeys(t, m)

	if err := m.DeleteRange([]byte("k10"), []byte("k05")); !errors.Is(err, storage.ErrInvalidRange) {
		t.Fatalf("Manager.DeleteRange(k10, k05) = %v, want ErrInvalidRange", err)
	}
	b := m.Batch()
	err := b.DeleteRange(nil, []byte("k05"))
	var batchErr *storage.BatchError
	if !errors.As(err, &batchErr) || batchErr.Op != "delete_range" || !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("batch DeleteRange(nil, k05) = %v, want a delete_range BatchError over ErrEmptyKey", err)
	}
	_ = b.Close()
	if err := b.DeleteRange([]byte("k00"), []byte("k05")); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("DeleteRange on a closed batch = %v, want ErrStorageClosed", err)
	}
	if got := managerKeys(t, m); len(got) != 20 {
		t.Fatalf("a refused range delete removed keys: %v", got)
	}

	_ = m.Close()
	if err := m.DeleteRange([]byte("k00"), []byte("k05")); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("DeleteRange on a closed manager = %v, want ErrStorageClosed", err)
	}
}

func TestDeleteRange_PartitionErrorPropagates(t *testing.T) {
	rangeErr := errors.New("partition range delete error")
	m := &Manager{
		router:     NewHashRouter(2),
		partitions: []storage.Storage{newTestMemoryStorage(), &failingStorage{deleteRangeErr: rangeErr}},
		config:     &Config{PartitionCount: 2, BackendType: BackendMemory},
	}
	defer func() { _ = m.Close() }()

	if err := m.DeleteRange([]byte("a"), []byte("b")); !errors.Is(err, rangeErr) {
		t.Fatalf("Manager.DeleteRange = %v, want the partition's error", err)
	}
	b := m.Batch()
	defer func() { _ = b.Close() }()
	if err := b.DeleteRange([]byte("a"), []byte("b")); !errors.Is(err, rangeErr) {
		t.Fatalf("batch DeleteRange = %v, want the partition batch's error", err)
	}
}
