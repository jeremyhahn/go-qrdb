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

package sqlitevfs_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	sdksqlitevfs "github.com/jeremyhahn/go-qrdb/sdk/go/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/sdk/go/standalone"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const pageSize = 4096

// newClient starts an in-process qrdb server on the real MVCC state machine
// and lock service, and returns its SDK client.
func newClient(t *testing.T) (transport.MVCCClient, transport.TransactionClient, transport.LockClient) {
	t.Helper()
	ctx := context.Background()
	srv, err := standalone.NewServer(ctx, &standalone.ServerConfig{Backend: database.BackendMemory, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("standalone.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(ctx) })
	c := srv.Client()
	return c.MVCC(), c.Transaction(), c.Lock()
}

func newBackend(t *testing.T, mvcc transport.MVCCClient, txn transport.TransactionClient) *sqlitevfs.ServiceBackend {
	t.Helper()
	b, err := sdksqlitevfs.NewBackend(mvcc, txn)
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	return b
}

func newStore(t *testing.T, b sqlitevfs.Backend, opts ...sqlitevfs.Option) *sqlitevfs.Store {
	t.Helper()
	s, err := sqlitevfs.NewStore(b, "app.db", opts...)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func pagesOf(fills ...byte) []byte {
	var out []byte
	for _, f := range fills {
		out = append(out, bytes.Repeat([]byte{f}, pageSize)...)
	}
	return out
}

func begin(t *testing.T, s *sqlitevfs.Store) *sqlitevfs.Txn {
	t.Helper()
	txn, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return txn
}

func writePage(t *testing.T, txn *sqlitevfs.Txn, pgno int64, fill byte) {
	t.Helper()
	if n, err := txn.WriteAt(pagesOf(fill), (pgno-1)*pageSize); err != nil || n != pageSize {
		t.Fatalf("WriteAt page %d = %d, %v", pgno, n, err)
	}
}

func writePages(t *testing.T, txn *sqlitevfs.Txn, fills ...byte) {
	t.Helper()
	for i, fill := range fills {
		writePage(t, txn, int64(i+1), fill)
	}
}

func readPage(t *testing.T, txn *sqlitevfs.Txn, pgno int64) []byte {
	t.Helper()
	buf := make([]byte, pageSize)
	if n, err := txn.ReadAt(buf, (pgno-1)*pageSize); err != nil || n != pageSize {
		t.Fatalf("ReadAt page %d = %d, %v", pgno, n, err)
	}
	return buf
}

func readAll(t *testing.T, txn *sqlitevfs.Txn) []byte {
	t.Helper()
	buf := make([]byte, txn.Size())
	if n, err := txn.ReadAt(buf, 0); err != nil || int64(n) != txn.Size() {
		t.Fatalf("ReadAt whole file = %d, %v", n, err)
	}
	return buf
}

func commit(t *testing.T, txn *sqlitevfs.Txn) int64 {
	t.Helper()
	rev, err := txn.Commit(t.Context())
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return rev
}

// countingMVCC counts the MovePrefix calls that reach the server.
type countingMVCC struct {
	transport.MVCCClient
	moves int
}

func (c *countingMVCC) MovePrefix(ctx context.Context, req *transport.MovePrefixRequest) (int64, error) {
	c.moves++
	return c.MVCCClient.MovePrefix(ctx, req)
}

// failingMVCC fails reads and moves with the errors a transport returns.
type failingMVCC struct {
	transport.MVCCClient
	getErr  error
	moveErr error
}

func (f *failingMVCC) GetAtRevision(ctx context.Context, key string, rev int64, opts ...transport.ReadOption) (*transport.MVCCKeyValue, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.MVCCClient.GetAtRevision(ctx, key, rev, opts...)
}

func (f *failingMVCC) MovePrefix(ctx context.Context, req *transport.MovePrefixRequest) (int64, error) {
	if f.moveErr != nil {
		return 0, f.moveErr
	}
	return f.MVCCClient.MovePrefix(ctx, req)
}

// failingTxn fails every transaction with the error a transport returns.
type failingTxn struct {
	transport.TransactionClient
	err error
}

func (f *failingTxn) ExecuteTransaction(ctx context.Context, req *transport.TransactionRequest, opts ...transport.WriteOption) (*transport.TransactionResponse, error) {
	return nil, f.err
}

func TestClientBackend_RefusesNil(t *testing.T) {
	mvcc, txn, _ := newClient(t)
	if _, err := sdksqlitevfs.NewBackend(nil, txn); !errors.Is(err, sqlitevfs.ErrNilBackend) {
		t.Fatalf("NewBackend(nil, txn) = %v, want ErrNilBackend", err)
	}
	if _, err := sdksqlitevfs.NewBackend(mvcc, nil); !errors.Is(err, sqlitevfs.ErrNilBackend) {
		t.Fatalf("NewBackend(mvcc, nil) = %v, want ErrNilBackend", err)
	}
}

func TestClientBackend_CommitAndRead(t *testing.T) {
	mvcc, txn, _ := newClient(t)
	b := newBackend(t, mvcc, txn)
	s := newStore(t, b, sqlitevfs.WithOptimistic())

	w := begin(t, s)
	writePages(t, w, 0x01, 0x02)
	rev := commit(t, w)
	if got, err := b.Revision(t.Context()); err != nil || got != rev {
		t.Fatalf("Revision() = %d, %v; want %d", got, err, rev)
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
		t.Fatal("a commit over the SDK client does not read back byte for byte")
	}

	w = begin(t, s)
	writePage(t, w, 2, 0x22)
	commit(t, w)
	old, err := s.BeginAt(t.Context(), rev)
	if err != nil {
		t.Fatalf("BeginAt(%d): %v", rev, err)
	}
	if got := readAll(t, old); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
		t.Fatal("a read at the first revision does not see the first commit")
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x01, 0x22)) {
		t.Fatal("the second commit does not read back")
	}
}

func TestClientBackend_StagedPublishOneRevision(t *testing.T) {
	mvcc, txn, _ := newClient(t)
	counting := &countingMVCC{MVCCClient: mvcc}
	b := newBackend(t, counting, txn)
	s := newStore(t, b, sqlitevfs.WithOptimistic(), sqlitevfs.WithStageThreshold(2*pageSize), sqlitevfs.WithStageChunkBytes(pageSize))

	seed := begin(t, s)
	writePages(t, seed, 0x01, 0x02)
	commit(t, seed)
	if counting.moves != 0 {
		t.Fatalf("a 2-page commit published %d times, want 0", counting.moves)
	}

	w := begin(t, s)
	readPage(t, w, 1)
	writePages(t, w, 0x11, 0x12, 0x13)
	rev := commit(t, w)
	if counting.moves != 1 {
		t.Fatalf("a staged commit published %d times, want 1", counting.moves)
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x11, 0x12, 0x13)) {
		t.Fatal("a staged commit does not read back byte for byte")
	}
	root, err := sqlitevfs.StageRoot("app.db")
	if err != nil {
		t.Fatalf("StageRoot: %v", err)
	}
	if keys, err := b.Keys(t.Context(), root, rev); err != nil || len(keys) != 0 {
		t.Fatalf("stage keys left live = %q, %v", keys, err)
	}
}

func TestClientBackend_SnapshotChanged(t *testing.T) {
	mvcc, txn, _ := newClient(t)
	b := newBackend(t, mvcc, txn)
	s := newStore(t, b, sqlitevfs.WithOptimistic())
	seed := begin(t, s)
	writePages(t, seed, 0x01, 0x02)
	commit(t, seed)

	reader, writer := begin(t, s), begin(t, s)
	readPage(t, reader, 1)
	writePage(t, reader, 2, 0x32)
	writePage(t, writer, 1, 0x41)
	commit(t, writer)
	rev, err := b.Revision(t.Context())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if _, err := reader.Commit(t.Context()); !errors.Is(err, sqlitevfs.ErrSnapshotChanged) {
		t.Fatalf("Commit over a changed read = %v, want ErrSnapshotChanged", err)
	}
	if got, _ := b.Revision(t.Context()); got != rev {
		t.Fatalf("a refused commit wrote: revision %d, want %d", got, rev)
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x41, 0x02)) {
		t.Fatal("the winning commit does not read back")
	}
}

func TestClientWriterLock_SecondHolderBusy(t *testing.T) {
	_, _, l := newClient(t)
	a, err := sdksqlitevfs.NewWriterLock(l, "a", 0)
	if err != nil {
		t.Fatalf("NewWriterLock(a): %v", err)
	}
	b, err := sdksqlitevfs.NewWriterLock(l, "b", 0)
	if err != nil {
		t.Fatalf("NewWriterLock(b): %v", err)
	}
	ctx := t.Context()
	const key = "sqlite/app.db/writer"
	token, err := a.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := b.Acquire(ctx, key); !errors.Is(err, sqlitevfs.ErrBusy) {
		t.Fatalf("held Acquire = %v, want ErrBusy", err)
	}
	if err := a.Check(ctx, key, token); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := b.Check(ctx, key, token+100); !errors.Is(err, sqlitevfs.ErrBusy) {
		t.Fatalf("Check with a wrong token = %v, want ErrBusy", err)
	}
	if err := a.Release(ctx, key, token); err != nil {
		t.Fatalf("Release: %v", err)
	}
	again, err := b.Acquire(ctx, key)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if again <= token {
		t.Fatalf("fencing token %d after %d, want it to rise", again, token)
	}
}

func TestClientWriterLock_DefaultTTL(t *testing.T) {
	_, _, l := newClient(t)
	w, err := sdksqlitevfs.NewWriterLock(l, "a", 0)
	if err != nil {
		t.Fatalf("NewWriterLock: %v", err)
	}
	const key = "sqlite/app.db/writer"
	if _, err := w.Acquire(t.Context(), key); err != nil {
		t.Fatalf("Acquire with the default ttl: %v", err)
	}
	info, err := l.LockStatus(t.Context(), key)
	if err != nil || info == nil {
		t.Fatalf("LockStatus = %+v, %v", info, err)
	}
	if want := int64(sdksqlitevfs.DefaultWriterLockTTL / time.Second); info.TTL != want {
		t.Fatalf("lock TTL = %d s, want %d s", info.TTL, want)
	}
	if _, err := sdksqlitevfs.NewWriterLock(l, "a", 500*time.Millisecond); !errors.Is(err, sqlitevfs.ErrInvalidOption) {
		t.Fatalf("NewWriterLock(500ms) = %v, want ErrInvalidOption", err)
	}
	if _, err := sdksqlitevfs.NewWriterLock(nil, "a", 0); !errors.Is(err, sqlitevfs.ErrNilBackend) {
		t.Fatalf("NewWriterLock(nil) = %v, want ErrNilBackend", err)
	}
}

// exceededOnWire is the shape a network transport hands a quota refusal in.
func exceededOnWire() error {
	ex := &quota.ExceededError{TenantID: "tenant-a", Resource: quota.ResourceStorageBytes, Current: 100, Limit: 64}
	return &transport.RequestError{
		Method:     "grpc",
		StatusCode: 8,
		Reason:     quota.ReasonQuotaExceeded,
		Metadata:   ex.Metadata(),
		Err:        errors.New(ex.Error()),
	}
}

func TestClientBackend_FullOverQuota(t *testing.T) {
	mvcc, txn, _ := newClient(t)
	b := newBackend(t, mvcc, &failingTxn{TransactionClient: txn, err: exceededOnWire()})
	s := newStore(t, b, sqlitevfs.WithOptimistic())
	w := begin(t, s)
	writePages(t, w, 0x01)
	_, err := w.Commit(t.Context())
	if !errors.Is(err, sqlitevfs.ErrFull) {
		t.Fatalf("Commit past quota = %v, want ErrFull", err)
	}
	ex, ok := errors.AsType[*quota.ExceededError](err)
	if !ok || ex.TenantID != "tenant-a" || ex.Resource != quota.ResourceStorageBytes || ex.Current != 100 || ex.Limit != 64 {
		t.Fatalf("ExceededError = %+v, %v; want tenant-a storage bytes 100/64", ex, ok)
	}
}

func TestClientBackend_ErrorMapping(t *testing.T) {
	guard := &service.WriteGuardRefusedError{Key: "sqlite/app.db/page/1"}
	conflict := &service.CommandConflictError{Message: "prefix count moved"}
	exceeded := &quota.ExceededError{TenantID: "t", Resource: quota.ResourceStorageBytes, Current: 2, Limit: 1}
	denied := &quota.EntitlementError{TenantID: "t", Feature: "sqlite_vfs"}
	embedded := func(raw error) error { return &transport.RequestError{Method: "embedded", Err: raw} }
	wire := func(status int, reason string, md map[string]string) error {
		return &transport.RequestError{Method: "rest", StatusCode: status, Reason: reason, Metadata: md, Err: errors.New("refused")}
	}
	tests := []struct {
		name     string
		err      error
		as       func(error) bool
		sentinel error
	}{
		{"guard embedded", embedded(guard), isType[*service.WriteGuardRefusedError], sqlitevfs.ErrSnapshotChanged},
		{"guard wire", wire(409, service.ReasonWriteGuardRefused, guard.Metadata()), isType[*service.WriteGuardRefusedError], sqlitevfs.ErrSnapshotChanged},
		{"conflict embedded", embedded(conflict), isType[*service.CommandConflictError], sqlitevfs.ErrPublishConflict},
		{"conflict wire", wire(409, service.ReasonCommandConflict, conflict.Metadata()), isType[*service.CommandConflictError], sqlitevfs.ErrPublishConflict},
		{"quota embedded", embedded(exceeded), isType[*quota.ExceededError], sqlitevfs.ErrFull},
		{"quota wire", wire(429, quota.ReasonQuotaExceeded, exceeded.Metadata()), isType[*quota.ExceededError], sqlitevfs.ErrFull},
		{"entitlement embedded", embedded(denied), isType[*quota.EntitlementError], nil},
		{"entitlement wire", wire(403, quota.ReasonEntitlementDenied, denied.Metadata()), isType[*quota.EntitlementError], nil},
	}
	mvcc, txn, _ := newClient(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackend(t, &failingMVCC{MVCCClient: mvcc, moveErr: tc.err}, txn)
			_, err := b.Publish(t.Context(), []byte("sqlite/app.db/stage/x/"), []byte("sqlite/app.db/page/"), 1, nil, map[string][]byte{"k": []byte("v")}, nil)
			if err == nil {
				t.Fatal("Publish succeeded, want the refusal")
			}
			if !tc.as(err) {
				t.Fatalf("Publish error %v does not carry the server's typed refusal", err)
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Fatalf("Publish error %v, want %v", err, tc.sentinel)
			}
		})
	}

	t.Run("a status without a reason is not a refusal", func(t *testing.T) {
		b := newBackend(t, &failingMVCC{MVCCClient: mvcc, moveErr: wire(409, "", nil)}, txn)
		_, err := b.Publish(t.Context(), []byte("src/"), []byte("dst/"), 1, nil, nil, nil)
		if isType[*service.WriteGuardRefusedError](err) || isType[*service.CommandConflictError](err) {
			t.Fatalf("a bare 409 was read as a typed refusal: %v", err)
		}
	})

	for name, notFound := range map[string]error{
		"key not found":      &transport.RequestError{Method: "embedded", Err: &transport.KeyNotFoundError{Key: "k"}},
		"revision not found": &transport.RevisionNotFoundError{Revision: 1},
	} {
		t.Run(name, func(t *testing.T) {
			b := newBackend(t, &failingMVCC{MVCCClient: mvcc, getErr: notFound}, txn)
			value, found, err := b.GetAt(t.Context(), []byte("k"), 1)
			if err != nil || found || value != nil {
				t.Fatalf("GetAt = %q, %v, %v; want not found", value, found, err)
			}
		})
	}
}

func isType[T error](err error) bool {
	_, ok := errors.AsType[T](err)
	return ok
}
