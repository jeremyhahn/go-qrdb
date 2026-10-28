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
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSecretFile writes data to a fresh file under t.TempDir() with mode.
func writeSecretFile(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod secret file: %v", err)
	}
	return path
}

// handleHolds reports whether h holds exactly want.
func handleHolds(t *testing.T, h interface{ EqualTo([]byte) (bool, error) }, want string) bool {
	t.Helper()
	ok, err := h.EqualTo([]byte(want))
	if err != nil {
		t.Fatalf("EqualTo: %v", err)
	}
	return ok
}

func TestReadFileHandle_ReadsSecret(t *testing.T) {
	cases := map[string]struct {
		contents string
		want     string
	}{
		"no line ending":   {contents: "fake-secret", want: "fake-secret"},
		"trailing LF":      {contents: "fake-secret\n", want: "fake-secret"},
		"trailing CRLF":    {contents: "fake-secret\r\n", want: "fake-secret"},
		"only one LF trim": {contents: "fake-secret\n\n", want: "fake-secret\n"},
		"interior newline": {contents: "fake\nsecret", want: "fake\nsecret"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeSecretFile(t, []byte(tc.contents), 0o600)
			h, err := ReadFileHandle(path)
			if err != nil {
				t.Fatalf("ReadFileHandle: %v", err)
			}
			defer func() { _ = h.Destroy() }()
			if h.Size() != len(tc.want) {
				t.Fatalf("Size = %d, want %d", h.Size(), len(tc.want))
			}
			if !handleHolds(t, h, tc.want) {
				t.Fatal("handle does not hold the file's secret")
			}
		})
	}
}

func TestReadFileHandle_ReadOnlyOwnerModeAccepted(t *testing.T) {
	path := writeSecretFile(t, []byte("fake-secret"), 0o400)
	h, err := ReadFileHandle(path)
	if err != nil {
		t.Fatalf("ReadFileHandle(0400): %v", err)
	}
	if err := h.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if h.IsAlive() {
		t.Fatal("handle alive after Destroy")
	}
}

func TestReadFileHandle_RefusesGroupOrOtherAccess(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o660, 0o644} {
		path := writeSecretFile(t, []byte("fake-secret"), mode)
		_, err := ReadFileHandle(path)
		var modeErr *FileModeError
		if !errors.As(err, &modeErr) {
			t.Fatalf("mode %v: err = %v, want *FileModeError", mode, err)
		}
		if modeErr.Path != path || modeErr.Mode != mode {
			t.Fatalf("mode %v: error = %+v", mode, modeErr)
		}
		if strings.Contains(err.Error(), "fake-secret") {
			t.Fatal("error carries the secret")
		}
	}
}

func TestReadFileHandle_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		path  string
		cause error
	}{
		"missing":      {path: filepath.Join(dir, "absent"), cause: fs.ErrNotExist},
		"directory":    {path: dir, cause: ErrSecretFileNotRegular},
		"empty":        {path: writeSecretFile(t, nil, 0o600), cause: ErrSecretFileEmpty},
		"only newline": {path: writeSecretFile(t, []byte("\r\n"), 0o600), cause: ErrSecretFileEmpty},
		"too large":    {path: writeSecretFile(t, bytes.Repeat([]byte("x"), MaxFileHandleSize+1), 0o600), cause: ErrSecretFileTooLarge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, err := ReadFileHandle(tc.path)
			if h != nil {
				t.Fatal("a handle was returned with the error")
			}
			var fileErr *FileHandleError
			if !errors.As(err, &fileErr) || fileErr.Path != tc.path {
				t.Fatalf("err = %v, want *FileHandleError naming %q", err, tc.path)
			}
			if !errors.Is(err, tc.cause) {
				t.Fatalf("err = %v, want cause %v", err, tc.cause)
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("error %q does not name the path", err)
			}
		})
	}
}

func TestReadFileHandle_MaxSizeAccepted(t *testing.T) {
	path := writeSecretFile(t, bytes.Repeat([]byte("k"), MaxFileHandleSize), 0o600)
	h, err := ReadFileHandle(path)
	if err != nil {
		t.Fatalf("ReadFileHandle at the bound: %v", err)
	}
	defer func() { _ = h.Destroy() }()
	if h.Size() != MaxFileHandleSize {
		t.Fatalf("Size = %d, want %d", h.Size(), MaxFileHandleSize)
	}
}

func TestSecretLen(t *testing.T) {
	cases := map[string]int{
		"":        0,
		"\n":      0,
		"\r\n":    0,
		"\r":      1,
		"a":       1,
		"a\n":     1,
		"a\r\n":   1,
		"a\n\n":   2,
		"a\r\r\n": 2,
	}
	for in, want := range cases {
		if got := secretLen([]byte(in)); got != want {
			t.Errorf("secretLen(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestFileHandleErrors_NameThePath(t *testing.T) {
	cause := errors.New("cause")
	fileErr := &FileHandleError{Path: "/p/key", Err: cause}
	if !errors.Is(fileErr, cause) {
		t.Fatal("FileHandleError does not unwrap to its cause")
	}
	for _, err := range []error{
		fileErr,
		&FileModeError{Path: "/p/key", Mode: 0o644},
		&FileOwnerError{Path: "/p/key"},
	} {
		if !strings.Contains(err.Error(), `"/p/key"`) {
			t.Errorf("%T.Error() = %q, does not name the path", err, err)
		}
	}
	if !strings.Contains((&FileModeError{Path: "p", Mode: 0o644}).Error(), "-rw-r--r--") {
		t.Error("FileModeError does not report the mode")
	}
}
