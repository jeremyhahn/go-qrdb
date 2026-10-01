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

package secure

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lyingSeeker reports offsets that do not match where the stream is. Every
// Seek answers `report`; the underlying reader really moves.
type lyingSeeker struct {
	*strings.Reader
	report int64
}

func (l *lyingSeeker) Seek(offset int64, whence int) (int64, error) {
	if _, err := l.Reader.Seek(offset, whence); err != nil {
		return 0, err
	}
	return l.report, nil
}

// TestSeekableStream_RequiresARegularFile pins the *os.File half of the probe:
// a character device answers lseek without error (a terminal driver may too),
// but it cannot give bytes back, so only a regular file takes the bulk path.
func TestSeekableStream_RequiresARegularFile(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()
	if _, err := devNull.Seek(0, io.SeekCurrent); err != nil {
		t.Fatalf("precondition: %s must accept a no-op seek for this test to mean anything: %v", os.DevNull, err)
	}
	if _, _, ok := seekableStream(devNull); ok {
		t.Fatalf("a character device was treated as seekable")
	}

	regular, err := os.Create(filepath.Join(t.TempDir(), "passphrase"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer regular.Close()
	if _, _, ok := seekableStream(regular); !ok {
		t.Fatalf("a regular file was not treated as seekable")
	}
}

// TestSeekableStream_PipeIsNotSeekable covers stdin as a pipe through a real
// *os.File: it is read byte-at-a-time and the rest of the pipe is untouched.
func TestSeekableStream_PipeIsNotSeekable(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	if _, err := io.WriteString(w, "hunter2\nrest"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()
	if _, _, ok := seekableStream(r); ok {
		t.Fatalf("a pipe was treated as seekable")
	}
	buf, err := NewStdinProviderFromReader(r).Passphrase(context.Background(), "")
	if err != nil {
		t.Fatalf("Passphrase: %v", err)
	}
	defer buf.Destroy()
	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(rest) != "rest" {
		t.Fatalf("remaining pipe = %q; a pipe must not be over-read", rest)
	}
}

// TestStdinProvider_RegularFileSeeksBackExactly drives the bulk path through a
// real regular file and checks where the file offset is left: exactly after
// the newline.
func TestStdinProvider_RegularFileSeeksBackExactly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passphrase")
	if err := os.WriteFile(path, []byte("hunter2\nthe-rest"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	buf, err := NewStdinProviderFromReader(f).Passphrase(context.Background(), "")
	if err != nil {
		t.Fatalf("Passphrase: %v", err)
	}
	defer buf.Destroy()
	if string(buf.Bytes()) != "hunter2" {
		t.Fatalf("passphrase = %q", buf.Bytes())
	}
	off, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("seek: %v", err)
	}
	if off != int64(len("hunter2\n")) {
		t.Fatalf("file offset = %d after the read, want %d", off, len("hunter2\n"))
	}
}

// TestStdinProvider_SeekBackToTheWrongOffsetIsAnError covers a stream whose
// Seek reports a position other than the one the read ended at. Reporting
// success would leave the next reader at an offset nobody checked.
func TestStdinProvider_SeekBackToTheWrongOffsetIsAnError(t *testing.T) {
	ls := &lyingSeeker{Reader: strings.NewReader("hunter2\nrest"), report: 0}
	buf, err := NewStdinProviderFromReader(ls).Passphrase(context.Background(), "")
	if err == nil {
		buf.Destroy()
		t.Fatalf("a seek-back that lands at the wrong offset must fail the read")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) || !errors.Is(err, errSeekOffsetMismatch) {
		t.Fatalf("Passphrase = %v (%T), want *ProviderError wrapping errSeekOffsetMismatch", err, err)
	}
}

// TestSeekableStream_NegativeProbeOffsetIsRefused covers a probe that answers
// with an impossible offset: the stream is not trusted with the bulk path.
func TestSeekableStream_NegativeProbeOffsetIsRefused(t *testing.T) {
	if _, _, ok := seekableStream(&lyingSeeker{Reader: strings.NewReader("x\n"), report: -1}); ok {
		t.Fatalf("a probe reporting a negative offset was accepted")
	}
}
