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

package file_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// errApplyFault is the fault recordGate injects into a record write.
var errApplyFault = errors.New("injected record write fault")

// recordGate intercepts the first record write after it is armed: the
// creation of a record's temp file in a shard directory, which a batch
// commit reaches only once its journal is published. Armed to block, it
// closes entered and waits for release; armed to fail, it returns
// errApplyFault.
type recordGate struct {
	root    string
	armed   atomic.Bool
	fail    bool
	entered chan struct{}
	release chan struct{}
}

func newRecordGate(root string, fail bool) *recordGate {
	return &recordGate{root: root, fail: fail, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *recordGate) pass(name string) error {
	if filepath.Dir(name) == g.root || !g.armed.CompareAndSwap(true, false) {
		return nil
	}
	if g.fail {
		return errApplyFault
	}
	close(g.entered)
	<-g.release
	return nil
}

// gatedBaseFS is the base file system under encfs; its record writes pass
// through a recordGate.
type gatedBaseFS struct {
	filesys.FS
	gate *recordGate
}

func (fs gatedBaseFS) Create(name string) (filesys.File, error) {
	if err := fs.gate.pass(name); err != nil {
		return nil, err
	}
	return fs.FS.Create(name)
}

// gatedEncryptor encrypts through the barrier over a gated base, so the
// engine sees the barrier's quiesce gate exactly as atrest's would give it.
type gatedEncryptor struct {
	barrier *seal.StorageBarrier
	gate    *recordGate
}

func (g gatedEncryptor) Enabled() bool { return true }

func (g gatedEncryptor) Wrap(base filesys.FS) filesys.FS {
	return encfs.New(gatedBaseFS{FS: base, gate: g.gate}, g.barrier)
}

// openGated opens an encrypted engine at dir whose record writes pass
// through gate.
func openGated(dir string, b *seal.StorageBarrier, gate *recordGate) (*file.Engine, error) {
	return file.New(dir, file.WithEncryptor(gatedEncryptor{barrier: b, gate: gate}))
}

// heldBy names the leases on g with work in flight, without waiting.
func heldBy(g *quiesce.Gate) []string {
	var timeout *quiesce.DrainTimeoutError
	if errors.As(g.Drain(-1), &timeout) {
		return timeout.Stores
	}
	return nil
}

func reopenAfterUnseal(t *testing.T, dir string, b *seal.StorageBarrier, creds seal.Credentials) *file.Engine {
	t.Helper()
	if b.IsSealed() {
		if err := b.Unseal(context.Background(), creds); err != nil {
			t.Fatalf("Unseal: %v", err)
		}
	}
	e := openEncrypted(t, dir, b)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func assertValue(t *testing.T, e *file.Engine, key, want string) {
	t.Helper()
	got, err := e.Get([]byte(key))
	if err != nil {
		t.Fatalf("Get %q: %v", key, err)
	}
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("Get %q = %q; want %q", key, got, want)
	}
}

func assertNoJournal(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".commit-journal")); !os.IsNotExist(err) {
		t.Fatalf("commit journal left behind: %v", err)
	}
}

func TestEncrypted_SealDrainsABatchAppliedAfterItsJournal(t *testing.T) {
	b, creds := newUnsealedBarrier(t)
	dir := filepath.Join(t.TempDir(), "store")
	gate := newRecordGate(dir, false)
	e, err := openGated(dir, b, gate)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	batch := e.Batch()
	for _, k := range []string{"a", "b"} {
		if err := batch.Set([]byte(k), []byte("v-"+k)); err != nil {
			t.Fatalf("batch Set: %v", err)
		}
	}
	gate.armed.Store(true)
	committed := make(chan error, 1)
	go func() { committed <- batch.Commit() }()
	<-gate.entered

	if got := heldBy(b.Quiesce()); !slices.Equal(got, []string{dir}) {
		t.Fatalf("held with a batch applying = %v; want [%s]", got, dir)
	}
	sealed := make(chan error, 1)
	go func() { sealed <- b.Seal() }()
	for !b.Quiesce().Closed() {
		runtime.Gosched()
	}
	select {
	case err := <-sealed:
		t.Fatalf("Seal returned %v with a file batch applying", err)
	default:
	}
	if b.IsSealed() {
		t.Fatal("the barrier sealed with a file batch applying")
	}

	close(gate.release)
	if err := <-committed; err != nil {
		t.Fatalf("the batch in flight = %v", err)
	}
	if err := <-sealed; err != nil {
		t.Fatalf("Seal = %v", err)
	}
	if err := e.Set([]byte("c"), []byte("v-c")); !errors.Is(err, quiesce.ErrSealing) {
		t.Fatalf("Set while sealed = %v; want quiesce.ErrSealing", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := reopenAfterUnseal(t, dir, b, creds)
	assertValue(t, r, "a", "v-a")
	assertValue(t, r, "b", "v-b")
	assertNoJournal(t, dir)
}

func TestEncrypted_OpenOnAClosedGateRefuses(t *testing.T) {
	b, _ := newUnsealedBarrier(t)
	dir := filepath.Join(t.TempDir(), "store")
	b.Quiesce().Close()

	e, err := file.New(dir, file.WithEncryptor(gatedEncryptor{barrier: b, gate: newRecordGate(dir, false)}))
	if !errors.Is(err, quiesce.ErrSealing) {
		t.Fatalf("New on a closed gate = %v; want quiesce.ErrSealing", err)
	}
	if e != nil {
		t.Fatal("a refused New returned an engine")
	}
	if got := heldBy(b.Quiesce()); len(got) != 0 {
		t.Fatalf("held after a refused New = %v; want none", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a refused New left the store directory behind: %v", err)
	}

	b.Quiesce().Reopen()
	r := openEncrypted(t, dir, b)
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set after the gate reopened: %v", err)
	}
	if got := heldBy(b.Quiesce()); len(got) != 0 {
		t.Fatalf("held after the write = %v; want none", got)
	}
}

func TestEncrypted_OpenHoldsTheLeaseAcrossJournalRecovery(t *testing.T) {
	b, creds := newUnsealedBarrier(t)
	dir := filepath.Join(t.TempDir(), "store")

	// Leave a published journal behind: the batch's first record write fails.
	faulty := newRecordGate(dir, true)
	e, err := openGated(dir, b, faulty)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	batch := e.Batch()
	for _, k := range []string{"a", "b"} {
		if err := batch.Set([]byte(k), []byte("v-"+k)); err != nil {
			t.Fatalf("batch Set: %v", err)
		}
	}
	faulty.armed.Store(true)
	if err := batch.Commit(); !errors.Is(err, errApplyFault) {
		t.Fatalf("Commit = %v; want the injected fault", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".commit-journal")); err != nil {
		t.Fatalf("no journal left behind: %v", err)
	}

	// Reopen; recovery replays the journal and blocks on a record write.
	hold := newRecordGate(dir, false)
	hold.armed.Store(true)
	type opened struct {
		e   *file.Engine
		err error
	}
	done := make(chan opened, 1)
	go func() {
		r, err := openGated(dir, b, hold)
		done <- opened{r, err}
	}()
	<-hold.entered
	if got := heldBy(b.Quiesce()); !slices.Equal(got, []string{dir}) {
		t.Fatalf("held during journal recovery = %v; want [%s]", got, dir)
	}
	close(hold.release)
	o := <-done
	if o.err != nil {
		t.Fatalf("New with a journal to recover: %v", o.err)
	}
	if got := heldBy(b.Quiesce()); len(got) != 0 {
		t.Fatalf("held after recovery = %v; want none", got)
	}
	if err := o.e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := reopenAfterUnseal(t, dir, b, creds)
	assertValue(t, r, "a", "v-a")
	assertValue(t, r, "b", "v-b")
	assertNoJournal(t, dir)
}
