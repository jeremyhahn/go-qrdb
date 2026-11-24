package encfs

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// TestFramed_IntactLogOpensWithoutEncrypt opens intact, stamped append
// logs through a barrier that can no longer encrypt but still decrypts (an
// exhausted or superseded barrier): read-only Open and Stat must read the
// log, since only a pending or empty log needs a fresh header.
func TestFramed_IntactLogOpensWithoutEncrypt(t *testing.T) {
	fb := newFakeBarrier(t)
	payload := []byte("synced records that must stay readable")
	for _, name := range []string{"000007.log", "MANIFEST-000001"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			w, err := New(filesys.Default, fb).Create(path)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if _, err := w.Write(payload); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if err := w.Sync(); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			ro := New(filesys.Default, &nthFailBarrier{fakeBarrier: fb, ok: 0})
			f, err := ro.Open(path)
			if err != nil {
				t.Fatalf("Open with a barrier that cannot encrypt: %v", err)
			}
			defer func() { _ = f.Close() }()
			got := make([]byte, len(payload))
			if _, err := f.ReadAt(got, 0); err != nil && err != io.EOF {
				t.Fatalf("ReadAt: %v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("read %q, want %q", got, payload)
			}
			fi, err := ro.Stat(path)
			if err != nil {
				t.Fatalf("Stat with a barrier that cannot encrypt: %v", err)
			}
			if fi.Size() != int64(len(payload)) {
				t.Fatalf("Stat size = %d, want %d", fi.Size(), len(payload))
			}
		})
	}
}
