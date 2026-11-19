package encfs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
)

var errInjected = errors.New("injected fault")

// failReadAtFile fails its failOn-th ReadAt call with errInjected.
type failReadAtFile struct {
	filesys.File
	failOn int
	calls  int
}

func (f *failReadAtFile) ReadAt(p []byte, off int64) (int, error) {
	f.calls++
	if f.calls == f.failOn {
		return 0, errInjected
	}
	return f.File.ReadAt(p, off)
}

// failWriteFile fails every Write and WriteAt with errInjected.
type failWriteFile struct{ filesys.File }

func (failWriteFile) Write([]byte) (int, error)          { return 0, errInjected }
func (failWriteFile) WriteAt([]byte, int64) (int, error) { return 0, errInjected }

// failWriteFS hands out files whose writes fail.
type failWriteFS struct{ filesys.FS }

func (fs failWriteFS) OpenReadWrite(name string) (filesys.File, error) {
	f, err := fs.FS.OpenReadWrite(name)
	if err != nil {
		return nil, err
	}
	return failWriteFile{f}, nil
}

// nthFailBarrier encrypts ok times, then fails every Encrypt.
type nthFailBarrier struct {
	*fakeBarrier
	ok    int
	calls int
}

func (b *nthFailBarrier) Encrypt(p []byte) ([]byte, error) {
	b.calls++
	if b.calls > b.ok {
		return nil, errInjected
	}
	return b.fakeBarrier.Encrypt(p)
}

// writeFramedLog writes one synced frame per record to a new append log and
// returns the raw file bytes and the frames.
func writeFramedLog(t *testing.T, b barrierKEK, path string, records ...[]byte) ([]byte, []frameRef) {
	t.Helper()
	f, err := New(filesys.Default, b).Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, rec := range records {
		if _, err := f.Write(rec); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := f.Sync(); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}
	ef, ok := f.(*encryptedFile)
	if !ok || !ef.framed {
		t.Fatalf("want a framed file, got %T", f)
	}
	frames := append([]frameRef(nil), ef.frames...)
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	return raw, frames
}

func readAllAt(t *testing.T, fsys filesys.FS, path string) ([]byte, error) {
	t.Helper()
	f, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

func le32(n uint32) []byte { return binary.LittleEndian.AppendUint32(nil, n) }

func TestFramed_ScanFramesTails(t *testing.T) {
	b := newFakeBarrier(t)
	a, c := bytes.Repeat([]byte{'a'}, 100), bytes.Repeat([]byte{'c'}, 60)
	raw, frames := writeFramedLog(t, b, filepath.Join(t.TempDir(), "000001.log"), a, c)
	last := frames[1].phys
	flipLast := bytes.Clone(raw)
	flipLast[last+frameLenSize+10] ^= 0x01

	cases := []struct {
		name    string
		file    []byte
		want    []byte
		wantErr error
	}{
		{"clean", raw, append(bytes.Clone(a), c...), nil},
		{"torn length prefix", append(bytes.Clone(raw), 1, 2), append(bytes.Clone(a), c...), nil},
		{"zero length prefix", append(bytes.Clone(raw), 0, 0, 0, 0), append(bytes.Clone(a), c...), nil},
		{"zero fill past last write", append(bytes.Clone(raw), make([]byte, 5000)...), append(bytes.Clone(a), c...), nil},
		{"zero length then data", append(bytes.Clone(raw), 0, 0, 0, 0, 7), nil, ErrAuthFailed},
		{"length over BlockSize", append(append(bytes.Clone(raw), le32(BlockSize+1)...), 9, 9, 9, 9), nil, ErrAuthFailed},
		{"torn final frame", append(bytes.Clone(raw), append(le32(50), 1, 2, 3)...), append(bytes.Clone(a), c...), nil},
		{"unauthentic final frame", flipLast, a, nil},
		{"unauthentic final frame, zero fill", append(bytes.Clone(flipLast), make([]byte, 64)...), a, nil},
		{"unauthentic final frame, data after", append(bytes.Clone(flipLast), 0, 0, 1), nil, ErrAuthFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "000001.log")
			if err := os.WriteFile(path, tc.file, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readAllAt(t, New(filesys.Default, b), path)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				w, err := New(filesys.Default, b).OpenReadWrite(path)
				if !errors.Is(err, tc.wantErr) {
					if w != nil {
						_ = w.Close()
					}
					t.Fatalf("OpenReadWrite err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("read %d bytes, want %d", len(got), len(tc.want))
			}
		})
	}
}

// sweepReadAtFaults fails each ReadAt of an append-log open in turn: every
// fault must surface as the open's error, until a fault falls past the last
// ReadAt and the open succeeds.
func sweepReadAtFaults(t *testing.T, fsys *FS, path string, wantErr error) {
	t.Helper()
	for k := 1; ; k++ {
		base, err := filesys.Default.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		w := &failReadAtFile{File: base, failOn: k}
		_, err = fsys.openEncryptedRO(w, true)
		_ = base.Close()
		if w.calls < k {
			if !errors.Is(err, wantErr) && (wantErr != nil || err != nil) {
				t.Fatalf("open without a fault: err = %v, want %v", err, wantErr)
			}
			return
		}
		if !errors.Is(err, errInjected) {
			t.Fatalf("ReadAt fault %d: err = %v, want the injected fault", k, err)
		}
	}
}

func TestFramed_ReadAtFaultsSurface(t *testing.T) {
	b := newFakeBarrier(t)
	fsys := New(filesys.Default, b).(*FS)
	raw, frames := writeFramedLog(t, b, filepath.Join(t.TempDir(), "000003.log"),
		bytes.Repeat([]byte{'x'}, 300), bytes.Repeat([]byte{'y'}, 30))
	flipLast := bytes.Clone(raw)
	flipLast[frames[1].phys+frameLenSize+5] ^= 0x01
	hdr := freshHeaderBytes(t, b, "000003.log")

	cases := []struct {
		name    string
		file    []byte
		wantErr error
	}{
		{"frames", raw, nil},
		{"zero fill", append(bytes.Clone(raw), make([]byte, 9000)...), nil},
		{"bad length, data after", append(bytes.Clone(raw), 0, 0, 0, 0, 1), ErrAuthFailed},
		{"unauthentic final frame, zero fill", append(bytes.Clone(flipLast), make([]byte, 8)...), nil},
		{"short torn header", hdr[:3], nil},
		{"torn header past the fixed part", hdr[:fixedHeaderSize+2], nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "000003.log")
			if err := os.WriteFile(path, tc.file, 0o600); err != nil {
				t.Fatal(err)
			}
			sweepReadAtFaults(t, fsys, path, tc.wantErr)
		})
	}
}

func TestFramed_TornHeaderClassification(t *testing.T) {
	b := newFakeBarrier(t)
	fsys := New(filesys.Default, b).(*FS)
	hdr := freshHeaderBytes(t, b, "000004.log")
	fresh, err := fsys.freshFramedHeader()
	if err != nil {
		t.Fatal(err)
	}
	want := fresh.totalSize()
	badSize := bytes.Clone(hdr[:offHeaderSize+2])
	binary.LittleEndian.PutUint16(badSize[offHeaderSize:], uint16(want+1)) // #nosec G115 -- small header size
	badDEK := bytes.Clone(hdr[:offWrappedDEKLen+4])
	binary.LittleEndian.PutUint32(badDEK[offWrappedDEKLen:], uint32(want-fixedHeaderSize+1)) // #nosec G115 -- small header size

	cases := []struct {
		name string
		file []byte
		torn bool
	}{
		{"empty", nil, true},
		{"magic prefix", hdr[:2], true},
		{"through the DEK length", hdr[:offWrappedDEKLen+4], true},
		{"one byte short", hdr[:want-1], true},
		{"whole header", hdr[:want], false},
		{"other magic", []byte("QX"), false},
		{"header size mismatch", badSize, false},
		{"wrapped DEK length mismatch", badDEK, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "000004.log")
			if err := os.WriteFile(path, tc.file, 0o600); err != nil {
				t.Fatal(err)
			}
			base, err := filesys.Default.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = base.Close() }()
			h, err := fsys.tornFramedHeader(base, int64(len(tc.file)))
			if err != nil {
				t.Fatalf("tornFramedHeader: %v", err)
			}
			if got := h != nil; got != tc.torn {
				t.Fatalf("torn = %v, want %v", got, tc.torn)
			}
			if h != nil && !h.framed() {
				t.Fatal("torn header's fresh header is not framed")
			}
		})
	}
}

func TestFramed_TornHeaderBarrierFaults(t *testing.T) {
	fb := newFakeBarrier(t)
	hdr := freshHeaderBytes(t, fb, "000005.log")
	for _, ok := range []int{0, 1} {
		for _, cut := range []int{3, fixedHeaderSize + 2} {
			path := filepath.Join(t.TempDir(), "000005.log")
			if err := os.WriteFile(path, hdr[:cut], 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := New(filesys.Default, &nthFailBarrier{fakeBarrier: fb, ok: ok}).Open(path); !errors.Is(err, errInjected) {
				t.Errorf("Open ok=%d cut=%d: err = %v, want the injected fault", ok, cut, err)
			}
			if _, err := New(filesys.Default, &nthFailBarrier{fakeBarrier: fb, ok: ok}).OpenReadWrite(path); !errors.Is(err, errInjected) {
				t.Errorf("OpenReadWrite ok=%d cut=%d: err = %v, want the injected fault", ok, cut, err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, hdr[:cut]) { // #nosec G304 -- test file under t.TempDir
				t.Errorf("ok=%d cut=%d: refused open changed the file (err %v)", ok, cut, err)
			}
		}
	}
}

func TestFramed_FreshOverTornHeaderRefusals(t *testing.T) {
	b := newFakeBarrier(t)
	fsys := New(filesys.Default, b).(*FS)
	h, err := fsys.freshFramedHeader()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "000006.log")
	if err := os.WriteFile(path, []byte("QE"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, err := filesys.Default.OpenReadWrite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = base.Close() }()
	if _, err := fsys.freshOverTornHeader(base, h, int64(h.totalSize())); !errors.Is(err, ErrTruncatedHeader) {
		t.Errorf("file as long as the header: err = %v, want ErrTruncatedHeader", err)
	}
	if _, err := fsys.freshOverTornHeader(failWriteFile{base}, h, 2); !errors.Is(err, errInjected) {
		t.Errorf("header write fault: err = %v, want the injected fault", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "QE" { // #nosec G304 -- test file under t.TempDir
		t.Errorf("refused rewrite changed the file: %q (err %v)", got, err)
	}
}

func TestFramed_OpenReadWriteWriteFaults(t *testing.T) {
	b := newFakeBarrier(t)
	raw, _ := writeFramedLog(t, b, filepath.Join(t.TempDir(), "000007.log"), []byte("record"))

	// A torn final frame is zeroed before appends; a fault there refuses.
	path := filepath.Join(t.TempDir(), "000007.log")
	torn := append(bytes.Clone(raw), append(le32(40), 1, 2)...)
	if err := os.WriteFile(path, torn, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(failWriteFS{filesys.Default}, b).OpenReadWrite(path); !errors.Is(err, errInjected) {
		t.Errorf("zeroing a torn tail: err = %v, want the injected fault", err)
	}

	// A fresh file whose header write fails refuses.
	fresh := filepath.Join(t.TempDir(), "000008.log")
	if _, err := New(failWriteFS{filesys.Default}, b).OpenReadWrite(fresh); !errors.Is(err, errInjected) {
		t.Errorf("fresh header write: err = %v, want the injected fault", err)
	}

	// A frame write fault surfaces from Sync.
	path2 := filepath.Join(t.TempDir(), "000009.log")
	w, err := New(filesys.Default, b).Create(path2)
	if err != nil {
		t.Fatal(err)
	}
	ef := w.(*encryptedFile)
	real := ef.base
	ef.base = failWriteFile{real}
	if _, err := w.Write([]byte("lost")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Sync(); !errors.Is(err, errInjected) {
		t.Errorf("Sync with a frame write fault: err = %v, want the injected fault", err)
	}
	ef.base = real
	ef.tail = ef.tail[:0]
	_ = w.Close()
}

func TestBlockCrypto_FrameBounds(t *testing.T) {
	c, err := newBlockCrypto(make([]byte, dekSize))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, BlockSize + 1} {
		if _, err := c.sealFrame(make([]byte, n), 0); !errors.Is(err, ErrBlockTooLarge) {
			t.Errorf("sealFrame(%d bytes): err = %v, want ErrBlockTooLarge", n, err)
		}
	}
	frame, err := c.sealFrame([]byte("abc"), 4)
	if err != nil {
		t.Fatal(err)
	}
	sealed := frame[frameLenSize:]
	for _, tc := range []struct {
		name  string
		data  []byte
		index int64
		n     int
		want  error
	}{
		{"zero length", sealed, 4, 0, ErrBlockTooShort},
		{"over BlockSize", sealed, 4, BlockSize + 1, ErrBlockTooShort},
		{"length mismatch", sealed, 4, 4, ErrBlockTooShort},
		{"wrong index", sealed, 5, 3, ErrAuthFailed},
		{"ok", sealed, 4, 3, nil},
	} {
		pt, err := c.openFrame(tc.data, tc.index, tc.n)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
		if tc.want == nil && string(pt) != "abc" {
			t.Errorf("%s: plaintext %q", tc.name, pt)
		}
	}
	if _, err := newBlockCrypto(make([]byte, dekSize-1)); !errors.Is(err, ErrDEKWrongLength) {
		t.Errorf("short DEK: err = %v, want ErrDEKWrongLength", err)
	}
}

// writeFixedLog writes data to a fixed-size-layout file named as an append
// log (a file written before the framed layout) and returns its path.
func writeFixedLog(t *testing.T, b barrierKEK, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "000010.log")
	base, err := filesys.Default.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := newHeader(b) // no framed flag
	if err != nil {
		t.Fatal(err)
	}
	f, err := newWriteFile(base, h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFixedLog_LogicalSize(t *testing.T) {
	b := newFakeBarrier(t)
	block := bytes.Repeat([]byte{'b'}, BlockSize)
	tail := []byte("tail record")
	cases := []struct {
		name string
		data []byte
		cut  int // bytes cut off the end of the file
		want []byte
	}{
		{"empty", nil, 0, nil},
		{"one whole block", block, 0, block},
		{"block and tail", append(bytes.Clone(block), tail...), 0, append(bytes.Clone(block), tail...)},
		{"tail torn below the AEAD overhead", append(bytes.Clone(block), tail...), len(tail) + 1, block},
		{"tail torn inside the ciphertext", append(bytes.Clone(block), tail...), 1, block},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFixedLog(t, b, tc.data)
			if tc.cut > 0 {
				fi, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(path, fi.Size()-int64(tc.cut)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readAllAt(t, New(filesys.Default, b), path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("read %d bytes, want %d", len(got), len(tc.want))
			}
		})
	}
	t.Run("read faults", func(t *testing.T) {
		path := writeFixedLog(t, b, append(bytes.Clone(block), tail...))
		sweepReadAtFaults(t, New(filesys.Default, b).(*FS), path, nil)
	})
	t.Run("negative payload", func(t *testing.T) {
		var neg *NegativePayloadLengthError
		if _, err := (&encryptedFile{}).fixedLogLogicalSize(-1); !errors.As(err, &neg) {
			t.Fatalf("err = %v, want NegativePayloadLengthError", err)
		}
	})
}
