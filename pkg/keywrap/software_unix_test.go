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

//go:build unix

package keywrap

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestSoftwareProvider_FIFOKeyRefRefusedWithoutBlocking proves a key
// reference naming a named pipe (FIFO) planted inside the confined key
// directory is refused immediately, never attempted with os.Open -- which
// blocks a reader until some other process opens the FIFO's write end, a
// denial-of-service lever available to anything that can create a file
// inside the key directory. readKeyFile now Stats (via the Root) before
// ever calling Open, so a non-regular file never reaches Open at all.
//
// Run on a goroutine with a short, generous deadline rather than directly:
// if this regresses back to Open-before-Stat, the call hangs forever (no
// writer will ever appear), and a bare call would wedge this whole test
// binary rather than fail this one test.
func TestSoftwareProvider_FIFOKeyRefRefusedWithoutBlocking(t *testing.T) {
	p, dir := newTestProvider(t)
	fifoPath := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	type result struct {
		dek []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		dek, err := p.Unwrap(context.Background(), "fifo", make([]byte, 28))
		done <- result{dek: dek, err: err}
	}()

	select {
	case r := <-done:
		var accessErr *KeyAccessError
		if !errors.As(r.err, &accessErr) {
			t.Fatalf("Unwrap(fifo) err = %v, want *KeyAccessError", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Unwrap(fifo) did not return within 2s: readKeyFile blocked opening a FIFO with no writer")
	}
}
