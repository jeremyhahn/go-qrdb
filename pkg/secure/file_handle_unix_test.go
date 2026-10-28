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

//go:build unix

package secure

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-enclave"
)

// fifoDeadline bounds how long the FIFO test waits for ReadFileHandle. A
// blocking open never returns, so the bound turns that defect into a failure
// rather than a hung test binary; a correct open returns in microseconds.
const fifoDeadline = 10 * time.Second

// TestReadFileHandle_RefusesFIFOWithoutBlocking proves a named pipe with no
// writer is refused as not a regular file at once: a blocking open(2) would
// wait for a writer forever and stall startup.
func TestReadFileHandle_RefusesFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	type result struct {
		h   enclave.ByteAccess
		err error
	}
	done := make(chan result, 1)
	go func() {
		h, err := ReadFileHandle(path)
		done <- result{h: h, err: err}
	}()
	select {
	case r := <-done:
		if r.h != nil {
			t.Fatal("a handle was returned for a FIFO")
		}
		var fileErr *FileHandleError
		if !errors.As(r.err, &fileErr) || fileErr.Path != path {
			t.Fatalf("err = %v, want *FileHandleError naming %q", r.err, path)
		}
		if !errors.Is(r.err, ErrSecretFileNotRegular) {
			t.Fatalf("err = %v, want cause %v", r.err, ErrSecretFileNotRegular)
		}
	case <-time.After(fifoDeadline):
		t.Fatalf("ReadFileHandle(%q) blocked on a FIFO with no writer", path)
	}
}

// sysInfo is a FileInfo whose Sys is replaced, to present checkSecretOwner
// with an owner the test cannot create a file under.
type sysInfo struct {
	os.FileInfo
	sys any
}

func (s sysInfo) Sys() any { return s.sys }

// TestCheckSecretOwner proves a file whose fstat owner is not the process
// user is refused, and that the process user's own file passes.
func TestCheckSecretOwner(t *testing.T) {
	path := writeSecretFile(t, []byte("s3cret"), 0o600)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSecretOwner(path, info); err != nil {
		t.Fatalf("own file refused: %v", err)
	}
	stat := *info.Sys().(*syscall.Stat_t)
	stat.Uid++
	err = checkSecretOwner(path, sysInfo{FileInfo: info, sys: &stat})
	var ownerErr *FileOwnerError
	if !errors.As(err, &ownerErr) || ownerErr.Path != path {
		t.Fatalf("err = %v, want *FileOwnerError naming %q", err, path)
	}
	if err := checkSecretOwner(path, sysInfo{FileInfo: info}); err != nil {
		t.Fatalf("no Stat_t: err = %v, want nil", err)
	}
}
