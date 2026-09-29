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

package service

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb"

	"github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// Compile-time regression guard: the clustered raft.HostAdapter (which
// embeds *quicraft.Host and promotes its methods) must keep satisfying
// snapshotExporter. If either type's signature drifts — as happened once
// already with ImportSnapshot's trailing ...quicraft.ImportOption — this
// line stops compiling instead of every backup silently returning
// BackupUnsupportedError at runtime.
var _ snapshotExporter = (*raft.HostAdapter)(nil)

// mockSnapshotHost extends mockHost (defined in service_test.go, same
// package) with the snapshotExporter methods, so it satisfies both the
// full Host interface hostShardSnapshotProvider stores and the
// snapshotExporter interface its type assertion probes for.
type mockSnapshotHost struct {
	*mockHost

	requestFn    func(ctx context.Context, shardID uint64, opt quicraft.SnapshotOption) (uint64, error)
	exportFn     func(ctx context.Context, shardID uint64, w io.Writer) error
	importFn     func(ctx context.Context, shardID uint64, r io.Reader, opts ...quicraft.ImportOption) error
	membershipFn func(ctx context.Context, shardID uint64) (*quicraft.Membership, error)

	requestCalls    atomic.Int32
	exportCalls     atomic.Int32
	importCalls     atomic.Int32
	membershipCalls atomic.Int32
}

var (
	_ Host             = (*mockSnapshotHost)(nil)
	_ snapshotExporter = (*mockSnapshotHost)(nil)
)

func newMockSnapshotHost() *mockSnapshotHost {
	return &mockSnapshotHost{mockHost: &mockHost{}}
}

func (m *mockSnapshotHost) SyncRequestSnapshot(ctx context.Context, shardID uint64, opt quicraft.SnapshotOption) (uint64, error) {
	m.requestCalls.Add(1)
	if m.requestFn != nil {
		return m.requestFn(ctx, shardID, opt)
	}
	return 1, nil
}

func (m *mockSnapshotHost) ExportSnapshot(ctx context.Context, shardID uint64, w io.Writer) error {
	m.exportCalls.Add(1)
	if m.exportFn != nil {
		return m.exportFn(ctx, shardID, w)
	}
	_, err := w.Write([]byte("snapshot-data"))
	return err
}

func (m *mockSnapshotHost) ImportSnapshot(ctx context.Context, shardID uint64, r io.Reader, opts ...quicraft.ImportOption) error {
	m.importCalls.Add(1)
	if m.importFn != nil {
		return m.importFn(ctx, shardID, r, opts...)
	}
	_, err := io.ReadAll(r)
	return err
}

// membershipFn, when set, overrides SyncGetShardMembership's default
// single-voting-replica response, letting tests exercise the RF>1 guard.
func (m *mockSnapshotHost) SyncGetShardMembership(ctx context.Context, shardID uint64) (*quicraft.Membership, error) {
	m.membershipCalls.Add(1)
	if m.membershipFn != nil {
		return m.membershipFn(ctx, shardID)
	}
	return &quicraft.Membership{Nodes: map[uint64]string{1: "127.0.0.1:9100"}}, nil
}

// mockExporterOnlyHost implements snapshotExporter over the plain mockHost,
// whose membership answer a test controls (membershipErr makes it fail, and
// RestoreShardOnline must then fail closed rather than skip the RF guard).
type mockExporterOnlyHost struct {
	*mockHost
}

func newMockExporterOnlyHost() *mockExporterOnlyHost {
	return &mockExporterOnlyHost{mockHost: &mockHost{}}
}

func (m *mockExporterOnlyHost) SyncRequestSnapshot(_ context.Context, _ uint64, _ quicraft.SnapshotOption) (uint64, error) {
	return 1, nil
}

func (m *mockExporterOnlyHost) ExportSnapshot(_ context.Context, _ uint64, w io.Writer) error {
	_, err := w.Write([]byte("snapshot-data"))
	return err
}

func (m *mockExporterOnlyHost) ImportSnapshot(_ context.Context, _ uint64, r io.Reader, _ ...quicraft.ImportOption) error {
	_, err := io.ReadAll(r)
	return err
}

var (
	_ Host             = (*mockExporterOnlyHost)(nil)
	_ snapshotExporter = (*mockExporterOnlyHost)(nil)
)

// fakeShardRestarter is a test double for ShardRestarter, recording calls and
// letting tests inject stop/restart failures to exercise
// RestoreShardOnline's rollback behavior.
type fakeShardRestarter struct {
	stopFn    func(shardID uint64) error
	restartFn func(ctx context.Context, shardID uint64) error

	stopCalls    atomic.Int32
	restartCalls atomic.Int32
}

func (f *fakeShardRestarter) StopForRestore(shardID uint64) error {
	f.stopCalls.Add(1)
	if f.stopFn != nil {
		return f.stopFn(shardID)
	}
	return nil
}

func (f *fakeShardRestarter) RestartAfterRestore(ctx context.Context, shardID uint64) error {
	f.restartCalls.Add(1)
	if f.restartFn != nil {
		return f.restartFn(ctx, shardID)
	}
	return nil
}

var _ ShardRestarter = (*fakeShardRestarter)(nil)

// ---------------------------------------------------------------------------
// SnapshotShard tests
// ---------------------------------------------------------------------------

func TestHostShardSnapshotProvider_SnapshotShard_RequestsBeforeExport(t *testing.T) {
	host := newMockSnapshotHost()

	var order []string
	host.requestFn = func(_ context.Context, _ uint64, _ quicraft.SnapshotOption) (uint64, error) {
		order = append(order, "request")
		return 7, nil
	}
	host.exportFn = func(_ context.Context, _ uint64, w io.Writer) error {
		order = append(order, "export")
		_, err := w.Write([]byte("payload"))
		return err
	}

	provider := NewHostShardSnapshotProvider(host)
	data, err := provider.SnapshotShard(context.Background(), 5)
	if err != nil {
		t.Fatalf("SnapshotShard returned error: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("expected payload bytes, got %q", data)
	}
	if host.requestCalls.Load() != 1 {
		t.Fatalf("expected SyncRequestSnapshot called once, got %d", host.requestCalls.Load())
	}
	if host.exportCalls.Load() != 1 {
		t.Fatalf("expected ExportSnapshot called once, got %d", host.exportCalls.Load())
	}
	if len(order) != 2 || order[0] != "request" || order[1] != "export" {
		t.Fatalf("expected snapshot request BEFORE export, got order %v", order)
	}
}

func TestHostShardSnapshotProvider_SnapshotShard_Unsupported(t *testing.T) {
	// A bare *mockHost does not implement snapshotExporter (no
	// SyncRequestSnapshot/ExportSnapshot/ImportSnapshot), mirroring the
	// non-Raft LocalBackend.
	host := &mockHost{}
	provider := NewHostShardSnapshotProvider(host)

	_, err := provider.SnapshotShard(context.Background(), 5)
	if err == nil {
		t.Fatal("expected error for host that does not support snapshots")
	}
	var unsupported *BackupUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected BackupUnsupportedError, got %T: %v", err, err)
	}
	if unsupported.Op != "snapshot" || unsupported.ShardID != 5 {
		t.Fatalf("unexpected BackupUnsupportedError fields: %+v", unsupported)
	}
}

func TestHostShardSnapshotProvider_SnapshotShard_RequestError(t *testing.T) {
	host := newMockSnapshotHost()
	requestErr := errors.New("request failed")
	host.requestFn = func(_ context.Context, _ uint64, _ quicraft.SnapshotOption) (uint64, error) {
		return 0, requestErr
	}

	provider := NewHostShardSnapshotProvider(host)
	_, err := provider.SnapshotShard(context.Background(), 9)
	if err == nil {
		t.Fatal("expected error when SyncRequestSnapshot fails")
	}
	var snapErr *ShardSnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected ShardSnapshotError, got %T: %v", err, err)
	}
	if snapErr.Op != "request" {
		t.Fatalf("expected Op=request, got %q", snapErr.Op)
	}
	if !errors.Is(err, requestErr) {
		t.Fatal("expected wrapped request error to unwrap")
	}
	if host.exportCalls.Load() != 0 {
		t.Fatalf("expected ExportSnapshot never called after request failure, got %d calls", host.exportCalls.Load())
	}
}

func TestHostShardSnapshotProvider_SnapshotShard_SnapshotInProgressTolerated(t *testing.T) {
	// A concurrent snapshot request (e.g. Raft's own auto-snapshot) already
	// holds the shard's snapshot slot. SnapshotShard must NOT fail here —
	// it proceeds to export, which succeeds because the concurrent
	// snapshot already landed.
	host := newMockSnapshotHost()
	host.requestFn = func(_ context.Context, _ uint64, _ quicraft.SnapshotOption) (uint64, error) {
		return 0, quicraft.ErrSnapshotInProgress
	}

	provider := NewHostShardSnapshotProvider(host)
	data, err := provider.SnapshotShard(context.Background(), 3)
	if err != nil {
		t.Fatalf("expected ErrSnapshotInProgress to be tolerated, got error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected exported snapshot bytes")
	}
	if host.exportCalls.Load() != 1 {
		t.Fatalf("expected ExportSnapshot called once, got %d", host.exportCalls.Load())
	}
}

func TestHostShardSnapshotProvider_SnapshotShard_SnapshotOutOfDateTolerated(t *testing.T) {
	// A back-to-back backup with no writes in between: the second
	// SyncRequestSnapshot call finds nothing new to save (LastApplied
	// unchanged since the earlier snapshot) and LogDB correctly refuses to
	// re-save at the same index. SnapshotShard must NOT fail here — the
	// earlier snapshot is still current, so it proceeds straight to export.
	host := newMockSnapshotHost()
	host.requestFn = func(_ context.Context, _ uint64, _ quicraft.SnapshotOption) (uint64, error) {
		return 0, logdb.ErrSnapshotOutOfDate
	}

	provider := NewHostShardSnapshotProvider(host)
	data, err := provider.SnapshotShard(context.Background(), 3)
	if err != nil {
		t.Fatalf("expected logdb.ErrSnapshotOutOfDate to be tolerated, got error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected exported snapshot bytes")
	}
	if host.exportCalls.Load() != 1 {
		t.Fatalf("expected ExportSnapshot called once, got %d", host.exportCalls.Load())
	}
}

func TestHostShardSnapshotProvider_SnapshotShard_ExportError(t *testing.T) {
	host := newMockSnapshotHost()
	exportErr := errors.New("export failed")
	host.exportFn = func(_ context.Context, _ uint64, _ io.Writer) error {
		return exportErr
	}

	provider := NewHostShardSnapshotProvider(host)
	_, err := provider.SnapshotShard(context.Background(), 11)
	if err == nil {
		t.Fatal("expected error when ExportSnapshot fails")
	}
	var snapErr *ShardSnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected ShardSnapshotError, got %T: %v", err, err)
	}
	if snapErr.Op != "export" {
		t.Fatalf("expected Op=export, got %q", snapErr.Op)
	}
	if !errors.Is(err, exportErr) {
		t.Fatal("expected wrapped export error to unwrap")
	}
}

func TestHostShardSnapshotProvider_SnapshotShard_ExportPollsUntilAvailable(t *testing.T) {
	// Simulates the ErrSnapshotInProgress race: the snapshot is not yet
	// visible to ExportSnapshot on the first couple of attempts, then
	// lands. SnapshotShard must retry rather than fail immediately.
	host := newMockSnapshotHost()
	host.requestFn = func(_ context.Context, _ uint64, _ quicraft.SnapshotOption) (uint64, error) {
		return 0, quicraft.ErrSnapshotInProgress
	}
	var attempts atomic.Int32
	host.exportFn = func(_ context.Context, _ uint64, w io.Writer) error {
		n := attempts.Add(1)
		if n < 3 {
			return quicraft.ErrSnapshotNotFound
		}
		_, err := w.Write([]byte("landed"))
		return err
	}

	provider := NewHostShardSnapshotProvider(host,
		WithSnapshotPollInterval(time.Millisecond),
		WithSnapshotPollTimeout(time.Second),
	)
	data, err := provider.SnapshotShard(context.Background(), 4)
	if err != nil {
		t.Fatalf("SnapshotShard returned error: %v", err)
	}
	if string(data) != "landed" {
		t.Fatalf("expected landed snapshot bytes, got %q", data)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 export attempts, got %d", attempts.Load())
	}
}

func TestHostShardSnapshotProvider_SnapshotShard_ExportTimesOut(t *testing.T) {
	host := newMockSnapshotHost()
	host.requestFn = func(_ context.Context, _ uint64, _ quicraft.SnapshotOption) (uint64, error) {
		return 0, quicraft.ErrSnapshotInProgress
	}
	host.exportFn = func(_ context.Context, _ uint64, _ io.Writer) error {
		return quicraft.ErrSnapshotNotFound
	}

	provider := NewHostShardSnapshotProvider(host,
		WithSnapshotPollInterval(time.Millisecond),
		WithSnapshotPollTimeout(10*time.Millisecond),
	)
	_, err := provider.SnapshotShard(context.Background(), 6)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var timeoutErr *ShardSnapshotTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected ShardSnapshotTimeoutError, got %T: %v", err, err)
	}
	if timeoutErr.ShardID != 6 {
		t.Fatalf("expected ShardID=6, got %d", timeoutErr.ShardID)
	}
}

func TestHostShardSnapshotProvider_SnapshotShard_ContextCanceledDuringPoll(t *testing.T) {
	host := newMockSnapshotHost()
	host.requestFn = func(_ context.Context, _ uint64, _ quicraft.SnapshotOption) (uint64, error) {
		return 0, quicraft.ErrSnapshotInProgress
	}
	host.exportFn = func(_ context.Context, _ uint64, _ io.Writer) error {
		return quicraft.ErrSnapshotNotFound
	}

	provider := NewHostShardSnapshotProvider(host,
		WithSnapshotPollInterval(50*time.Millisecond),
		WithSnapshotPollTimeout(time.Minute),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	_, err := provider.SnapshotShard(ctx, 8)
	if err == nil {
		t.Fatal("expected error when context is canceled during poll")
	}
	var snapErr *ShardSnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected ShardSnapshotError, got %T: %v", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected wrapped context.Canceled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// RestoreShard tests
// ---------------------------------------------------------------------------

func TestHostShardSnapshotProvider_RestoreShard_Success(t *testing.T) {
	host := newMockSnapshotHost()
	provider := NewHostShardSnapshotProvider(host)

	err := provider.RestoreShard(context.Background(), 2, []byte("snapshot-bytes"))
	if err != nil {
		t.Fatalf("RestoreShard returned error: %v", err)
	}
	if host.importCalls.Load() != 1 {
		t.Fatalf("expected ImportSnapshot called once, got %d", host.importCalls.Load())
	}
}

func TestHostShardSnapshotProvider_RestoreShard_Unsupported(t *testing.T) {
	host := &mockHost{}
	provider := NewHostShardSnapshotProvider(host)

	err := provider.RestoreShard(context.Background(), 2, []byte("data"))
	if err == nil {
		t.Fatal("expected error for host that does not support restore")
	}
	var unsupported *BackupUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected BackupUnsupportedError, got %T: %v", err, err)
	}
	if unsupported.Op != "restore" {
		t.Fatalf("expected Op=restore, got %q", unsupported.Op)
	}
}

func TestHostShardSnapshotProvider_RestoreShard_ImportError(t *testing.T) {
	host := newMockSnapshotHost()
	importErr := errors.New("import failed")
	host.importFn = func(_ context.Context, _ uint64, _ io.Reader, _ ...quicraft.ImportOption) error {
		return importErr
	}
	provider := NewHostShardSnapshotProvider(host)

	err := provider.RestoreShard(context.Background(), 2, []byte("data"))
	if err == nil {
		t.Fatal("expected error when ImportSnapshot fails")
	}
	var snapErr *ShardSnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected ShardSnapshotError, got %T: %v", err, err)
	}
	if snapErr.Op != "import" {
		t.Fatalf("expected Op=import, got %q", snapErr.Op)
	}
	if !errors.Is(err, importErr) {
		t.Fatal("expected wrapped import error to unwrap")
	}
}

// ---------------------------------------------------------------------------
// RestoreShardOnline tests
// ---------------------------------------------------------------------------

func TestHostShardSnapshotProvider_RestoreShardOnline_SystemShardRefused(t *testing.T) {
	host := newMockSnapshotHost()
	restarter := &fakeShardRestarter{}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(restarter))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), shard.ShardKV, []byte("data"))
	if err == nil {
		t.Fatal("expected error restoring the system shard online")
	}
	var sysErr *OnlineRestoreSystemShardError
	if !errors.As(err, &sysErr) {
		t.Fatalf("expected OnlineRestoreSystemShardError, got %T: %v", err, err)
	}
	if sysErr.ShardID != shard.ShardKV {
		t.Fatalf("expected ShardID=%d, got %d", shard.ShardKV, sysErr.ShardID)
	}
	// The system-shard guard must reject BEFORE any membership check, stop,
	// or import is attempted.
	if host.membershipCalls.Load() != 0 {
		t.Fatalf("expected no membership query for the system shard, got %d", host.membershipCalls.Load())
	}
	if host.importCalls.Load() != 0 {
		t.Fatalf("expected no import attempt for the system shard, got %d", host.importCalls.Load())
	}
	if restarter.stopCalls.Load() != 0 {
		t.Fatalf("expected no stop attempt for the system shard, got %d", restarter.stopCalls.Load())
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_Unsupported_NoSnapshotExporter(t *testing.T) {
	host := &mockHost{}
	provider := NewHostShardSnapshotProvider(host)

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	var unsupported *BackupUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected BackupUnsupportedError, got %T: %v", err, err)
	}
	if unsupported.Op != "restore" {
		t.Fatalf("expected Op=restore, got %q", unsupported.Op)
	}
}

// TestHostShardSnapshotProvider_RestoreShardOnline_MembershipUnreadable pins
// that the RF=1 guard fails closed: a membership that cannot be read is an
// error, never a skipped guard.
func TestHostShardSnapshotProvider_RestoreShardOnline_MembershipUnreadable(t *testing.T) {
	host := newMockExporterOnlyHost()
	host.membershipErr = errors.New("no leader")
	restarter := &fakeShardRestarter{}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(restarter))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	var snapErr *ShardSnapshotError
	if !errors.As(err, &snapErr) || !errors.Is(err, host.membershipErr) {
		t.Fatalf("expected ShardSnapshotError wrapping the membership failure, got %T: %v", err, err)
	}
	if snapErr.ShardID != 1000 {
		t.Fatalf("expected ShardID=1000, got %d", snapErr.ShardID)
	}
	if restarter.stopCalls.Load() != 0 {
		t.Fatalf("expected no stop attempt when membership cannot be queried, got %d", restarter.stopCalls.Load())
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_ReplicatedShardRefused(t *testing.T) {
	host := newMockSnapshotHost()
	host.membershipFn = func(_ context.Context, _ uint64) (*quicraft.Membership, error) {
		return &quicraft.Membership{
			Nodes: map[uint64]string{1: "127.0.0.1:9101", 2: "127.0.0.1:9102", 3: "127.0.0.1:9103"},
		}, nil
	}
	restarter := &fakeShardRestarter{}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(restarter))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	var repErr *OnlineRestoreReplicatedShardError
	if !errors.As(err, &repErr) {
		t.Fatalf("expected OnlineRestoreReplicatedShardError, got %T: %v", err, err)
	}
	if repErr.VotingReplicas != 3 {
		t.Fatalf("expected VotingReplicas=3, got %d", repErr.VotingReplicas)
	}
	if host.importCalls.Load() != 0 {
		t.Fatalf("expected no import attempt for a replicated shard, got %d", host.importCalls.Load())
	}
	if restarter.stopCalls.Load() != 0 {
		t.Fatalf("expected no stop attempt for a replicated shard, got %d", restarter.stopCalls.Load())
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_ReplicatedShard_WitnessesCountAsVoters(t *testing.T) {
	host := newMockSnapshotHost()
	host.membershipFn = func(_ context.Context, _ uint64) (*quicraft.Membership, error) {
		return &quicraft.Membership{
			Nodes:     map[uint64]string{1: "127.0.0.1:9101"},
			Witnesses: map[uint64]string{2: "127.0.0.1:9102"},
		}, nil
	}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(&fakeShardRestarter{}))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	var repErr *OnlineRestoreReplicatedShardError
	if !errors.As(err, &repErr) {
		t.Fatalf("expected OnlineRestoreReplicatedShardError (1 node + 1 witness = 2 voters), got %T: %v", err, err)
	}
	if repErr.VotingReplicas != 2 {
		t.Fatalf("expected VotingReplicas=2, got %d", repErr.VotingReplicas)
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_MembershipQueryError(t *testing.T) {
	host := newMockSnapshotHost()
	membershipErr := errors.New("read index failed")
	host.membershipFn = func(_ context.Context, _ uint64) (*quicraft.Membership, error) {
		return nil, membershipErr
	}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(&fakeShardRestarter{}))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	var snapErr *ShardSnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected ShardSnapshotError, got %T: %v", err, err)
	}
	if snapErr.Op != "membership" {
		t.Fatalf("expected Op=membership, got %q", snapErr.Op)
	}
	if !errors.Is(err, membershipErr) {
		t.Fatal("expected wrapped membership error to unwrap")
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_NoRestarterConfigured(t *testing.T) {
	host := newMockSnapshotHost()
	// No WithShardRestarter option: RF=1 (default single-node membership) but
	// nothing can safely restart the shard after stopping it.
	provider := NewHostShardSnapshotProvider(host)

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	var unsup *OnlineRestoreUnsupportedError
	if !errors.As(err, &unsup) {
		t.Fatalf("expected OnlineRestoreUnsupportedError, got %T: %v", err, err)
	}
	if host.importCalls.Load() != 0 {
		t.Fatalf("expected no import attempt with no restarter configured, got %d", host.importCalls.Load())
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_Success(t *testing.T) {
	host := newMockSnapshotHost()
	restarter := &fakeShardRestarter{}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(restarter))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("snapshot-bytes"))
	if err != nil {
		t.Fatalf("RestoreShardOnline returned error: %v", err)
	}
	if restarter.stopCalls.Load() != 1 {
		t.Fatalf("expected StopForRestore called once, got %d", restarter.stopCalls.Load())
	}
	if host.importCalls.Load() != 1 {
		t.Fatalf("expected ImportSnapshot called once, got %d", host.importCalls.Load())
	}
	if restarter.restartCalls.Load() != 1 {
		t.Fatalf("expected RestartAfterRestore called once, got %d", restarter.restartCalls.Load())
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_IdempotentOutOfDateStillRestarts(t *testing.T) {
	host := newMockSnapshotHost()
	host.importFn = func(_ context.Context, _ uint64, _ io.Reader, _ ...quicraft.ImportOption) error {
		return logdb.ErrSnapshotOutOfDate
	}
	restarter := &fakeShardRestarter{}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(restarter))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	if err != nil {
		t.Fatalf("expected logdb.ErrSnapshotOutOfDate to be tolerated as already-applied, got error: %v", err)
	}
	if restarter.stopCalls.Load() != 1 {
		t.Fatalf("expected StopForRestore called once, got %d", restarter.stopCalls.Load())
	}
	if restarter.restartCalls.Load() != 1 {
		t.Fatalf("expected RestartAfterRestore called once even for the idempotent no-op, got %d", restarter.restartCalls.Load())
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_ImportFails_RollsBackAndRestarts(t *testing.T) {
	host := newMockSnapshotHost()
	importErr := errors.New("import failed")
	host.importFn = func(_ context.Context, _ uint64, _ io.Reader, _ ...quicraft.ImportOption) error {
		return importErr
	}
	restarter := &fakeShardRestarter{}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(restarter))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	if err == nil {
		t.Fatal("expected error when ImportSnapshot fails")
	}
	var snapErr *ShardSnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected ShardSnapshotError, got %T: %v", err, err)
	}
	if snapErr.Op != "import" {
		t.Fatalf("expected Op=import, got %q", snapErr.Op)
	}
	if !errors.Is(err, importErr) {
		t.Fatal("expected wrapped import error to unwrap")
	}
	// The shard must be restarted even though the import failed: a failed
	// restore must never leave the shard down.
	if restarter.restartCalls.Load() != 1 {
		t.Fatalf("expected RestartAfterRestore called once as rollback, got %d", restarter.restartCalls.Load())
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_ImportAndRestartBothFail(t *testing.T) {
	host := newMockSnapshotHost()
	importErr := errors.New("import failed")
	host.importFn = func(_ context.Context, _ uint64, _ io.Reader, _ ...quicraft.ImportOption) error {
		return importErr
	}
	restartErr := errors.New("restart failed")
	restarter := &fakeShardRestarter{
		restartFn: func(_ context.Context, _ uint64) error { return restartErr },
	}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(restarter))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	var rollbackErr *OnlineRestoreRollbackError
	if !errors.As(err, &rollbackErr) {
		t.Fatalf("expected OnlineRestoreRollbackError, got %T: %v", err, err)
	}
	if !errors.Is(rollbackErr.ImportErr, importErr) {
		t.Fatalf("expected ImportErr=%v, got %v", importErr, rollbackErr.ImportErr)
	}
	if !errors.Is(rollbackErr.RestartErr, restartErr) {
		t.Fatalf("expected RestartErr=%v, got %v", restartErr, rollbackErr.RestartErr)
	}
	if !errors.Is(err, importErr) {
		t.Fatal("expected Unwrap to surface the import error")
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_StopFails(t *testing.T) {
	host := newMockSnapshotHost()
	stopErr := errors.New("stop failed")
	restarter := &fakeShardRestarter{
		stopFn: func(_ uint64) error { return stopErr },
	}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(restarter))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	var snapErr *ShardSnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected ShardSnapshotError, got %T: %v", err, err)
	}
	if snapErr.Op != "stop" {
		t.Fatalf("expected Op=stop, got %q", snapErr.Op)
	}
	if host.importCalls.Load() != 0 {
		t.Fatalf("expected no import attempt when stop fails, got %d", host.importCalls.Load())
	}
	if restarter.restartCalls.Load() != 0 {
		t.Fatalf("expected no restart attempt when stop itself failed, got %d", restarter.restartCalls.Load())
	}
}

func TestHostShardSnapshotProvider_RestoreShardOnline_RestartFailsAfterSuccessfulImport(t *testing.T) {
	host := newMockSnapshotHost()
	restartErr := errors.New("restart failed")
	restarter := &fakeShardRestarter{
		restartFn: func(_ context.Context, _ uint64) error { return restartErr },
	}
	provider := NewHostShardSnapshotProvider(host, WithShardRestarter(restarter))

	err := provider.(OnlineShardRestorer).RestoreShardOnline(context.Background(), 1000, []byte("data"))
	var snapErr *ShardSnapshotError
	if !errors.As(err, &snapErr) {
		t.Fatalf("expected ShardSnapshotError, got %T: %v", err, err)
	}
	if snapErr.Op != "restart" {
		t.Fatalf("expected Op=restart, got %q", snapErr.Op)
	}
	if !errors.Is(err, restartErr) {
		t.Fatal("expected wrapped restart error to unwrap")
	}
}

// ---------------------------------------------------------------------------
// AvailableShards tests
// ---------------------------------------------------------------------------

func TestHostShardSnapshotProvider_AvailableShards(t *testing.T) {
	host := newMockSnapshotHost()
	host.getNodeHostInfoFn = func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
		return &quicraft.NodeHostInfo{
			ShardInfoList: []quicraft.ShardInfo{
				{ShardID: 1},
				{ShardID: 2},
			},
		}
	}
	provider := NewHostShardSnapshotProvider(host)

	shards, err := provider.AvailableShards(context.Background())
	if err != nil {
		t.Fatalf("AvailableShards returned error: %v", err)
	}
	if len(shards) != 2 {
		t.Fatalf("expected 2 shards, got %d", len(shards))
	}
}

func TestHostShardSnapshotProvider_AvailableShards_NilInfo(t *testing.T) {
	host := newMockSnapshotHost()
	host.getNodeHostInfoFn = func(_ quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
		return nil
	}
	provider := NewHostShardSnapshotProvider(host)

	shards, err := provider.AvailableShards(context.Background())
	if err != nil {
		t.Fatalf("AvailableShards returned error: %v", err)
	}
	if shards != nil {
		t.Fatalf("expected nil shards, got %v", shards)
	}
}

// ---------------------------------------------------------------------------
// Error message tests
// ---------------------------------------------------------------------------

func TestShardSnapshotErrors_Messages(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "BackupUnsupportedError",
			err:      &BackupUnsupportedError{Op: "snapshot", ShardID: 5},
			expected: "service: shard snapshot for shard 5 not supported on this host (backup/restore requires a clustered Raft host)",
		},
		{
			name:     "ShardSnapshotError",
			err:      &ShardSnapshotError{Op: "export", ShardID: 5, Err: errors.New("disk full")},
			expected: "service: shard snapshot export for shard 5: disk full",
		},
		{
			name:     "ShardSnapshotTimeoutError",
			err:      &ShardSnapshotTimeoutError{ShardID: 5, Timeout: 10 * time.Second},
			expected: "service: shard snapshot export for shard 5 timed out after 10s waiting for a concurrently requested snapshot to become available",
		},
		{
			name:     "OnlineRestoreSystemShardError",
			err:      &OnlineRestoreSystemShardError{ShardID: 5},
			expected: "service: online restore of shard 5 refused: it hosts the system tenant; stopping it while serving would take down auth for every subsequent admin call, including the one that would restart it — use the offline 'qrdbd start --restore-backup-id' boot flow instead",
		},
		{
			name:     "OnlineRestoreReplicatedShardError",
			err:      &OnlineRestoreReplicatedShardError{ShardID: 5, VotingReplicas: 3},
			expected: "service: online restore of shard 5 refused: it has 3 voting replicas; an online per-replica import would be silently overwritten by the majority — restoring a replicated shard requires an offline whole-cluster procedure",
		},
		{
			name:     "OnlineRestoreUnsupportedError",
			err:      &OnlineRestoreUnsupportedError{ShardID: 5, Reason: "no shard restarter is configured for this host"},
			expected: "service: online restore of shard 5 not supported: no shard restarter is configured for this host — use the offline 'qrdbd start --restore-backup-id' boot flow instead",
		},
		{
			name: "OnlineRestoreRollbackError",
			err: &OnlineRestoreRollbackError{
				ShardID:    5,
				ImportErr:  errors.New("import failed"),
				RestartErr: errors.New("restart failed"),
			},
			expected: "service: online restore of shard 5 failed to import (import failed) AND failed to restart the shard afterward (restart failed) — the shard may be DOWN; operator intervention required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestShardSnapshotError_Unwrap(t *testing.T) {
	inner := errors.New("inner error")
	err := &ShardSnapshotError{Op: "export", ShardID: 1, Err: inner}
	if !errors.Is(err, inner) {
		t.Fatal("expected Unwrap to return inner error")
	}
}

func TestOnlineRestoreRollbackError_Unwrap(t *testing.T) {
	importErr := errors.New("import failed")
	restartErr := errors.New("restart failed")
	err := &OnlineRestoreRollbackError{ShardID: 1, ImportErr: importErr, RestartErr: restartErr}
	if !errors.Is(err, importErr) {
		t.Fatal("expected Unwrap to return the import error")
	}
	if errors.Is(err, restartErr) {
		t.Fatal("did not expect Unwrap to surface the restart error (available via the RestartErr field)")
	}
}
