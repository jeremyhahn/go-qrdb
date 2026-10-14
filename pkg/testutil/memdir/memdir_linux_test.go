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

package memdir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTempDir_IsOnTmpfsAndRemovedAfterTheTest(t *testing.T) {
	var dir string
	t.Run("use", func(t *testing.T) {
		dir = TempDir(t)
		mem, err := IsMemory(dir)
		if err != nil || !mem {
			t.Fatalf("IsMemory(%s) = %v, %v; want a tmpfs directory", dir, mem, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
			t.Fatalf("the directory is not writable: %v", err)
		}
	})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Stat(%s) after the test = %v, want it removed", dir, err)
	}
}

func TestTempDir_EachCallIsAFreshDirectory(t *testing.T) {
	a, b := TempDir(t), TempDir(t)
	if a == b {
		t.Fatalf("two calls returned the same directory %s", a)
	}
}

func TestIsMemory(t *testing.T) {
	if mem, err := IsMemory("/proc"); err != nil || mem {
		t.Fatalf("IsMemory(/proc) = %v, %v; want false: procfs is not tmpfs", mem, err)
	}
	if _, err := IsMemory(filepath.Join(memRoot, "no-such-dir-memdir-test")); !os.IsNotExist(err) {
		t.Fatalf("IsMemory(missing) error = %v, want not-exist", err)
	}
}

func TestNewDir_RefusesARootNotInMemory(t *testing.T) {
	_, err := newDir("/proc")
	var notMem *NotMemoryError
	if !errors.As(err, &notMem) || notMem.Path != "/proc" {
		t.Fatalf("newDir(/proc) = %v, want NotMemoryError for /proc", err)
	}
	if !strings.Contains(err.Error(), "/proc") {
		t.Fatalf("error %q does not name the path", err)
	}
}

// fatalRecorder is a testing.TB whose Fatalf and Errorf record the message
// instead of failing the test, and whose Cleanup collects the functions for
// the test to run, so the failure paths can be observed.
type fatalRecorder struct {
	testing.TB
	fatal, errored string
	cleanups       []func()
}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
}

func (r *fatalRecorder) Errorf(format string, args ...any) {
	r.errored = fmt.Sprintf(format, args...)
}

func (r *fatalRecorder) Cleanup(fn func()) { r.cleanups = append(r.cleanups, fn) }

func TestTempDirIn_ReportsACleanupThatCannotRemoveTheDirectory(t *testing.T) {
	rec := &fatalRecorder{TB: t}
	dir := tempDirIn(rec, memRoot)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	refused := errors.New("device busy")
	prev := removeAll
	removeAll = func(string) error { return refused }
	t.Cleanup(func() { removeAll = prev })
	for _, fn := range rec.cleanups {
		fn()
	}
	if !strings.Contains(rec.errored, dir) || !strings.Contains(rec.errored, refused.Error()) {
		t.Fatalf("Errorf message = %q, want the directory and the removal error", rec.errored)
	}
}

func TestTempDirIn_FailsTheTestOnARootNotInMemory(t *testing.T) {
	rec := &fatalRecorder{TB: t}
	if dir := tempDirIn(rec, "/proc"); dir != "" {
		t.Fatalf("tempDirIn(/proc) = %q, want no directory", dir)
	}
	if !strings.Contains(rec.fatal, "not on a memory-backed filesystem") {
		t.Fatalf("Fatalf message = %q, want the NotMemoryError", rec.fatal)
	}
}
