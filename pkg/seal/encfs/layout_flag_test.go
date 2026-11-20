package encfs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

// writeLegacyLog writes a fixed-size-layout append log holding one synced
// record, as a build before the framed layout left it.
func writeLegacyLog(t *testing.T, b barrierKEK, path string, rec []byte) {
	t.Helper()
	base, err := filesys.Default.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	f, err := newWriteFile(base, h)
	if err != nil {
		t.Fatalf("newWriteFile: %v", err)
	}
	if _, err := f.Write(rec); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestLayoutFlagFlipRefused flips the plaintext framed-layout flag of an
// append log: on a fixed-size log holding one synced 16-byte record it
// would otherwise open as an empty framed log whose read-write open
// overwrites the record, and on a framed log it would be read as
// fixed-size blocks. Both opens must refuse, and the file must be left as
// it was.
func TestLayoutFlagFlipRefused(t *testing.T) {
	b := newFakeBarrier(t)
	fs := New(filesys.Default, b)
	rec := []byte("acknowledged-rec") // one 16-byte record
	cases := []struct {
		name  string
		write func(path string)
		flags byte
	}{
		{"legacy flipped on", func(p string) { writeLegacyLog(t, b, p, rec) }, headerFlagFramed},
		{"framed flipped off", func(p string) { writeFramedLog(t, b, p, rec) }, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const logName = "000002.log"
			dir := t.TempDir()
			path := filepath.Join(dir, logName)
			tc.write(path)
			raw, err := os.ReadFile(path) // #nosec G304 -- path is under t.TempDir.
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if raw[offFlags] == tc.flags {
				t.Fatalf("flags byte already %#x", tc.flags)
			}
			raw[offFlags] = tc.flags
			writeInRoot(t, dir, logName, raw)
			opens := map[string]func(string) (filesys.File, error){
				"Open":          fs.Open,
				"OpenReadWrite": fs.OpenReadWrite,
			}
			for name, open := range opens {
				f, err := open(path)
				if err == nil {
					_ = f.Close()
					t.Errorf("%s of a flipped header opened", name)
					continue
				}
				var mismatch *HeaderLayoutMismatchError
				if !errors.As(err, &mismatch) || !errors.Is(err, ErrInvalidHeader) {
					t.Errorf("%s error = %v, want HeaderLayoutMismatchError", name, err)
				}
			}
			after, err := os.ReadFile(path) // #nosec G304 -- path is under t.TempDir.
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if !bytes.Equal(after, raw) {
				t.Fatalf("refused open changed the file: %d bytes, was %d", len(after), len(raw))
			}
		})
	}
}

// TestSealedFlagsZeroByteRefused covers a wrapped DEK whose sealed flags
// byte is present but zero: no build writes it, so it is corrupt.
func TestSealedFlagsZeroByteRefused(t *testing.T) {
	b := newFakeBarrier(t)
	h, err := newHeader(b)
	if err != nil {
		t.Fatalf("newHeader: %v", err)
	}
	wrapped, err := b.Encrypt(append(h.DEK[:], 0))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	h.WrappedDEK = wrapped
	if _, err := readHeader(bytes.NewReader(h.marshal()), b); !errors.Is(err, ErrCorruptDEK) {
		t.Fatalf("readHeader error = %v, want ErrCorruptDEK", err)
	}
}
