// Copyright 2026 Jeremy Hahn
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

package sqlitevfs

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// pageFailBackend fails every read of a page key of db, and nothing else.
type pageFailBackend struct {
	*MVCCBackend
	prefix []byte
	fail   bool
}

func (b *pageFailBackend) GetAt(ctx context.Context, key []byte, rev int64) ([]byte, bool, error) {
	if b.fail && bytes.HasPrefix(key, b.prefix) {
		return nil, false, errInjected
	}
	return b.MVCCBackend.GetAt(ctx, key, rev)
}

func TestStoreOptimistic(t *testing.T) {
	b := newTestBackend(t)
	if newTestStore(t, b, "app.db").Optimistic() {
		t.Fatal("a default Store reports optimistic")
	}
	s, err := NewStore(b, "app.db", WithOptimistic())
	if err != nil {
		t.Fatalf("NewStore(optimistic): %v", err)
	}
	if !s.Optimistic() {
		t.Fatal("an optimistic Store does not report optimistic")
	}
}

func TestStoreRestoreRefusesNegativeRevisions(t *testing.T) {
	ctx := t.Context()
	s := newTestStore(t, newTestBackend(t), "app.db")
	if _, err := s.RestoreToRevision(ctx, -1); err == nil {
		t.Fatal("RestoreToRevision(-1) succeeded")
	}
	if _, err := s.RestoreToRevisionFrom(ctx, -1, 0); err == nil {
		t.Fatal("RestoreToRevisionFrom(-1, 0) succeeded")
	}
	if _, err := s.RestoreToRevisionFrom(ctx, 0, -1); err == nil {
		t.Fatal("RestoreToRevisionFrom(0, -1) succeeded")
	}
}

func TestStoreRestoreBackendErrors(t *testing.T) {
	ctx := t.Context()
	b := &errBackend{MVCCBackend: newTestBackend(t)}
	s := newTestStore(t, b, "app.db")
	target := seed(t, s, 0x01)
	b.failGet = true
	if _, err := s.RestoreToRevision(ctx, target); !errors.Is(err, errInjected) {
		t.Fatalf("RestoreToRevision over a failing read = %v, want errInjected", err)
	}
	if _, err := s.RestoreToRevisionFrom(ctx, target, target); !errors.Is(err, errInjected) {
		t.Fatalf("RestoreToRevisionFrom over a failing read = %v, want errInjected", err)
	}
	b.failGet = false
	b.failKeys = true
	if _, err := s.RestoreToRevision(ctx, target); !errors.Is(err, errInjected) {
		t.Fatalf("RestoreToRevision over a failing stage sweep = %v, want errInjected", err)
	}
	b.failKeys = false
	b.failRevision = true
	if _, err := s.RestoreToRevision(ctx, target); !errors.Is(err, errInjected) {
		t.Fatalf("RestoreToRevision over a failing revision = %v, want errInjected", err)
	}
}

func TestStoreRestoreReserveBusy(t *testing.T) {
	ctx := t.Context()
	b := newTestBackend(t)
	locker := newMemLocker()
	sa := newLockedStore(t, b, locker, "a")
	sb := newLockedStore(t, b, locker, "b")
	target := seed(t, sa, 0x01)
	held := beginReserved(t, sa)
	defer func() { _ = held.Rollback(ctx) }()
	if _, err := sb.RestoreToRevision(ctx, target); !errors.Is(err, ErrBusy) {
		t.Fatalf("RestoreToRevision while another writer holds the lock = %v, want ErrBusy", err)
	}
}

func TestStoreRestoreFromAbsentDatabase(t *testing.T) {
	ctx := t.Context()
	b := newTestBackend(t)
	seed(t, newTestStore(t, b, "other.db"), 0x09)
	observed := revOf(t, b)
	s := newTestStore(t, b, "app.db")
	if got, err := s.RestoreToRevisionFrom(ctx, observed, observed); err != nil || got != observed {
		t.Fatalf("RestoreToRevisionFrom of a never-created database = %d, %v; want %d", got, err, observed)
	}
	seed(t, s, 0x01)
	before := revOf(t, b)
	if _, err := s.RestoreToRevisionFrom(ctx, observed, observed); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("RestoreToRevisionFrom past a create = %v, want ErrSnapshotChanged", err)
	}
	if revOf(t, b) != before {
		t.Fatal("a refused restore wrote a revision")
	}
	ebs := &errBackend{MVCCBackend: b}
	es := newTestStore(t, ebs, "new.db")
	ebs.failRevision = true
	if _, err := es.RestoreToRevisionFrom(ctx, observed, observed); !errors.Is(err, errInjected) {
		t.Fatalf("RestoreToRevisionFrom over a failing revision = %v, want errInjected", err)
	}
}

func TestStoreRestoreImageEdgeCases(t *testing.T) {
	ctx := t.Context()

	t.Run("an absent page restores as zeros", func(t *testing.T) {
		b := newTestBackend(t)
		s := newTestStore(t, b, "app.db")
		txn := begin(t, s)
		if _, err := txn.WriteAt(pageOf(0x03), 2*testPageSize); err != nil {
			t.Fatalf("WriteAt page 3: %v", err)
		}
		target := commit(t, txn)
		txn = begin(t, s)
		writePages(t, txn, 0x01, 0x02, 0x03)
		commit(t, txn)
		if _, err := s.RestoreToRevision(ctx, target); err != nil {
			t.Fatalf("RestoreToRevision: %v", err)
		}
		want := append(make([]byte, 2*testPageSize), pageOf(0x03)...)
		if got := imageAt(t, b, "app.db", revOf(t, b)); !bytes.Equal(got, want) {
			t.Fatal("the restored image does not hold zeros for the absent pages")
		}
	})

	t.Run("a page read failure fails the restore", func(t *testing.T) {
		mb := newTestBackend(t)
		prefix, err := PagePrefix("app.db")
		if err != nil {
			t.Fatalf("PagePrefix: %v", err)
		}
		b := &pageFailBackend{MVCCBackend: mb, prefix: prefix}
		s := newTestStore(t, b, "app.db")
		target := seed(t, s, 0x01)
		txn := begin(t, s)
		writePages(t, txn, 0x02)
		commit(t, txn)
		b.fail = true
		if _, err := s.RestoreToRevision(ctx, target); !errors.Is(err, errInjected) {
			t.Fatalf("RestoreToRevision over a failing page read = %v, want errInjected", err)
		}
	})

	t.Run("a page of the wrong size fails the restore", func(t *testing.T) {
		b := newTestBackend(t)
		s := newTestStore(t, b, "app.db")
		seed(t, s, 0x01)
		key, err := PageKey("app.db", 1)
		if err != nil {
			t.Fatalf("PageKey: %v", err)
		}
		target, err := b.Commit(ctx, nil, map[string][]byte{string(key): []byte("short")}, nil)
		if err != nil {
			t.Fatalf("Commit short page: %v", err)
		}
		if _, err := s.RestoreToRevision(ctx, target); err == nil {
			t.Fatal("RestoreToRevision restored a page of the wrong size")
		}
	})

	t.Run("an emptied database restores empty", func(t *testing.T) {
		b := newTestBackend(t)
		s := newTestStore(t, b, "app.db")
		seed(t, s, 0x01)
		txn := begin(t, s)
		if err := txn.Truncate(0); err != nil {
			t.Fatalf("Truncate(0): %v", err)
		}
		target := commit(t, txn)
		txn = begin(t, s)
		writePages(t, txn, 0x05)
		commit(t, txn)
		if _, err := s.RestoreToRevision(ctx, target); err != nil {
			t.Fatalf("RestoreToRevision: %v", err)
		}
		if got := imageAt(t, b, "app.db", revOf(t, b)); len(got) != 0 {
			t.Fatalf("restored image is %d bytes, want empty", len(got))
		}
	})

	t.Run("a different page size is replaced", func(t *testing.T) {
		b := newTestBackend(t)
		s := newTestStore(t, b, "app.db")
		target := seed(t, s, 0x01)
		txn := begin(t, s)
		if err := txn.Truncate(0); err != nil {
			t.Fatalf("Truncate(0): %v", err)
		}
		commit(t, txn)
		_, m := metaRecordAt(t, b, "app.db", revOf(t, b))
		m.PageSize = 2 * testPageSize
		m.PageCount = 0
		raw, err := m.MarshalBinary()
		if err != nil {
			t.Fatalf("MarshalBinary: %v", err)
		}
		key, err := MetaKey("app.db")
		if err != nil {
			t.Fatalf("MetaKey: %v", err)
		}
		if _, err := b.Commit(ctx, nil, map[string][]byte{string(key): raw}, nil); err != nil {
			t.Fatalf("Commit 8 KiB meta: %v", err)
		}
		s = newTestStore(t, b, "app.db")
		if _, err := s.RestoreToRevision(ctx, target); err != nil {
			t.Fatalf("RestoreToRevision: %v", err)
		}
		if got := imageAt(t, b, "app.db", revOf(t, b)); !bytes.Equal(got, pageOf(0x01)) {
			t.Fatal("the restore across a page size change does not read back")
		}
	})
}
