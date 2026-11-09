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

//go:build !integration

package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*File, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "LOCK")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, path
}

func TestOpen_CreatesPrivateFile(t *testing.T) {
	_, path := openTemp(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got&0o077 != 0 && runtime.GOOS != "windows" {
		t.Fatalf("lock file mode = %v, want no group/other access", got)
	}
}

func TestOpen_MissingDirectoryIsTypedError(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "absent", "LOCK"))
	var fe *Error
	if !errors.As(err, &fe) || fe.Op != "open" {
		t.Fatalf("Open error = %v, want *Error with Op open", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open error does not unwrap to os.ErrNotExist: %v", err)
	}
}

func TestLock_ExcludesAnotherHolderUntilUnlock(t *testing.T) {
	first, path := openTemp(t)
	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open second: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if err := first.Lock(); err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	acquired := make(chan error, 1)
	go func() { acquired <- second.Lock() }()

	select {
	case err := <-acquired:
		t.Fatalf("second Lock returned (%v) while the first holder held the lock", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := first.Unlock(); err != nil {
		t.Fatalf("first Unlock: %v", err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second Lock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second Lock did not proceed after the first holder unlocked")
	}
	if err := second.Unlock(); err != nil {
		t.Fatalf("second Unlock: %v", err)
	}
}

func TestLock_ReleasedByClose(t *testing.T) {
	first, path := openTemp(t)
	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open second: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if err := first.Lock(); err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	acquired := make(chan error, 1)
	go func() { acquired <- second.Lock() }()
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second Lock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("closing the holder did not release the lock")
	}
}

func TestTryLock_RefusedWhileAnotherHoldsAndTakenOnceReleased(t *testing.T) {
	first, path := openTemp(t)
	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open second: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if locked, err := first.TryLock(); err != nil || !locked {
		t.Fatalf("first TryLock = %v, %v; want true, nil", locked, err)
	}
	if locked, err := second.TryLock(); err != nil || locked {
		t.Fatalf("second TryLock while held = %v, %v; want false, nil", locked, err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("first Unlock: %v", err)
	}
	if locked, err := second.TryLock(); err != nil || !locked {
		t.Fatalf("second TryLock once released = %v, %v; want true, nil", locked, err)
	}
}

func TestTryLock_AfterCloseIsTypedError(t *testing.T) {
	l, _ := openTemp(t)
	_ = l.Close()
	_, err := l.TryLock()
	var lockErr *Error
	if !errors.As(err, &lockErr) || lockErr.Op != "lock" {
		t.Fatalf("TryLock after Close: err = %v, want *Error with Op lock", err)
	}
}

func TestLock_AfterCloseIsTypedError(t *testing.T) {
	l, _ := openTemp(t)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := l.Lock()
	var fe *Error
	if !errors.As(err, &fe) || fe.Op != "lock" {
		t.Fatalf("Lock after Close = %v, want *Error with Op lock", err)
	}
}

func TestUnlock_AfterCloseIsTypedError(t *testing.T) {
	l, _ := openTemp(t)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := l.Unlock()
	var fe *Error
	if !errors.As(err, &fe) || fe.Op != "unlock" {
		t.Fatalf("Unlock after Close = %v, want *Error with Op unlock", err)
	}
}

func TestClose_Twice_IsTypedError(t *testing.T) {
	l, _ := openTemp(t)
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	err := l.Close()
	var fe *Error
	if !errors.As(err, &fe) || fe.Op != "close" {
		t.Fatalf("second Close = %v, want *Error with Op close", err)
	}
	if fe.Error() == "" || fe.Unwrap() == nil {
		t.Fatal("Error must describe and unwrap its cause")
	}
}
