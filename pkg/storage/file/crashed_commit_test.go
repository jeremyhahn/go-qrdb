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

package file

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

const crashChildEnv = "QRDB_FILE_CRASH_CHILD"

// TestCrashedCommitHelper is the child side of
// TestLock_SurvivorFinishesCommitAbandonedByDeadProcess. It journals a batch and
// dies before applying it, without releasing anything: the kernel drops its lock.
// The parent runs it with the store directory as its working directory, so no
// path crosses the process boundary.
func TestCrashedCommitHelper(t *testing.T) {
	if os.Getenv(crashChildEnv) != "1" {
		return // not a child: nothing to do outside the parent's re-exec
	}
	e, err := New(".")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := e.lockWrites(); err != nil {
		t.Fatalf("lockWrites: %v", err)
	}
	ops := []storage.BufferedOp{
		{Key: []byte("a"), Value: []byte("from-dead-child")},
		{Key: []byte("b"), Value: []byte("from-dead-child")},
	}
	if err := e.writeJournal(encodeJournal(ops)); err != nil {
		t.Fatalf("writeJournal: %v", err)
	}
	os.Exit(0)
}

// A process that dies between writing a commit journal and applying it leaves
// the journal behind. A surviving process that already has the directory open
// must finish that commit when it next takes the lock, before journaling its
// own batch or writing: otherwise its batch overwrites the journal and the dead
// process's operations are lost, or a later reopen replays the stale journal
// over newer writes.
func TestLock_SurvivorFinishesCommitAbandonedByDeadProcess(t *testing.T) {
	dir := t.TempDir()
	survivor, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = survivor.Close() })

	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the argument is a compile-time literal. No shell is involved.
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashedCommitHelper$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), crashChildEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, journalFileName)); err != nil {
		t.Fatalf("child left no journal: %v", err)
	}

	b := survivor.Batch()
	if err := b.Set([]byte("c"), []byte("from-survivor")); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_ = b.Close()
	// A newer write over a key the dead commit touched must survive a reopen.
	if err := survivor.Set([]byte("a"), []byte("newer")); err != nil {
		t.Fatal(err)
	}
	if err := survivor.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for key, want := range map[string]string{"a": "newer", "b": "from-dead-child", "c": "from-survivor"} {
		got, err := reopened.Get([]byte(key))
		if err != nil || string(got) != want {
			t.Errorf("Get(%q) = %q, %v; want %q", key, got, err, want)
		}
	}
}
