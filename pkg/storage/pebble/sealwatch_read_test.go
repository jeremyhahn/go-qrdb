package pebble

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// sealReadsStore opens an in-memory Pebble database holding one key behind a
// store whose seal watch reports the barrier sealed while *sealed is set. A
// nil sealed builds a store without a watch.
func sealReadsStore(t *testing.T, sealed *atomic.Bool) *PebbleStorage {
	t.Helper()
	db, err := pebble.Open("", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Set([]byte("k/1"), []byte("marker"), pebble.NoSync); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if sealed == nil {
		return NewFromDB(db, false)
	}
	w := NewSealWatch(&recordingLogger{}, nil)
	w.barrierSealed = sealed.Load
	return NewFromDB(db, false, WithSealWatch(w))
}

// readEntryPoints runs every read entry point of s and returns each one's
// error, failing the test if a read returned the seeded marker alongside an
// error.
func readEntryPoints(t *testing.T, s *PebbleStorage) map[string]error {
	t.Helper()
	errs := map[string]error{}

	v, err := s.Get([]byte("k/1"))
	if err != nil && v != nil {
		t.Errorf("Get returned %q with error %v", v, err)
	}
	if err == nil && string(v) != "marker" {
		t.Errorf("Get = %q, want marker", v)
	}
	errs["Get"] = err

	keys, err := s.List([]byte("k/"))
	if err != nil && len(keys) != 0 {
		t.Errorf("List returned %d keys with error %v", len(keys), err)
	}
	if err == nil && len(keys) != 1 {
		t.Errorf("List = %d keys, want 1", len(keys))
	}
	errs["List"] = err

	ok, err := s.Exists([]byte("k/1"))
	if err != nil && ok {
		t.Errorf("Exists reported true with error %v", err)
	}
	if err == nil && !ok {
		t.Error("Exists = false, want true")
	}
	errs["Exists"] = err

	it := s.NewIterator(nil)
	valid := it.SeekToFirst()
	if it.Error() != nil && valid {
		t.Errorf("NewIterator is valid with error %v", it.Error())
	}
	if it.Error() == nil && (!valid || string(it.Value()) != "marker") {
		t.Errorf("NewIterator first = %v, want the marker", valid)
	}
	errs["NewIterator"] = it.Error()
	_ = it.Close()

	snap, err := s.Snapshot()
	if err != nil && snap != nil {
		t.Errorf("Snapshot returned a snapshot with error %v", err)
	}
	if snap != nil {
		_ = snap.Close()
	}
	errs["Snapshot"] = err
	return errs
}

func TestPebbleStorage_SealedBarrierRefusesReads(t *testing.T) {
	var sealed atomic.Bool
	s := sealReadsStore(t, &sealed)

	for op, err := range readEntryPoints(t, s) {
		if err != nil {
			t.Errorf("unsealed %s: %v", op, err)
		}
	}

	sealed.Store(true)
	for op, err := range readEntryPoints(t, s) {
		var sealedErr *SealedError
		if !errors.As(err, &sealedErr) {
			t.Errorf("sealed %s: err = %v, want a *SealedError", op, err)
			continue
		}
		if !errors.Is(err, ErrBarrierSealed) {
			t.Errorf("sealed %s: err = %v, want ErrBarrierSealed", op, err)
		}
	}

	sealed.Store(false)
	for op, err := range readEntryPoints(t, s) {
		if err != nil {
			t.Errorf("unsealed again %s: %v", op, err)
		}
	}
}

func TestPebbleStorage_StoreWithoutSealWatchReads(t *testing.T) {
	s := sealReadsStore(t, nil)
	for op, err := range readEntryPoints(t, s) {
		if err != nil {
			t.Errorf("%s: %v", op, err)
		}
	}
	var _ storage.Storage = s
}
