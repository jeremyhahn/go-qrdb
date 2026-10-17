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

package store_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

var storeLog = vlog.LogID{TenantID: "acme", Database: "certs", Log: "issued"}

func storeLogDescriptor() vlog.Descriptor {
	return vlog.Descriptor{Origin: "db.example.com/acme/certs", Hash: vlog.HashSHA256, VerifierKeys: []string{"key-1"}, AllowExpireBodies: true}
}

// logNode is one state machine of either family, opened over its own
// directory, that applies commands at increasing indexes.
type logNode struct {
	sm     sm.DiskStateMachine
	reader func() storage.Reader
	next   uint64
}

// openLogNode opens a state machine of kind in dir over logs, which may be
// nil for one built without a log registry.
func openLogNode(t *testing.T, kind auditSMKind, dir string, logs *vlog.Chain) *logNode {
	t.Helper()
	s, reader := kind.new(logs)
	if _, err := s.Open(context.Background(), dir, make(chan struct{})); err != nil {
		t.Fatalf("%s: open: %v", kind.name, err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return &logNode{sm: s, reader: reader, next: 1}
}

// update applies cmds as one Update call.
func (n *logNode) update(cmds ...[]byte) ([]sm.Result, error) {
	entries := make([]sm.Entry, len(cmds))
	for i, cmd := range cmds {
		entries[i] = sm.Entry{Index: n.next, Cmd: cmd}
		n.next++
	}
	results := make([]sm.Result, len(cmds))
	return results, n.sm.Update(context.Background(), entries, results)
}

// apply applies cmds as one Update call that must not halt.
func (n *logNode) apply(t *testing.T, cmds ...[]byte) []sm.Result {
	t.Helper()
	results, err := n.update(cmds...)
	if err != nil {
		t.Fatalf("Update = %v", err)
	}
	return results
}

// lookup answers query through the state machine's read path.
func (n *logNode) lookup(t *testing.T, query any) any {
	t.Helper()
	got, err := n.sm.Lookup(context.Background(), query)
	if err != nil {
		t.Fatalf("Lookup(%T) = %v", query, err)
	}
	return got
}

// enc fails t on an encode error and answers the encoded command.
func enc(t *testing.T) func([]byte, error) []byte {
	return func(cmd []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return cmd
	}
}

// table answers every record under prefix in r.
func table(t *testing.T, r storage.Reader, prefix string) map[string]string {
	t.Helper()
	it := r.NewIterator(&storage.IteratorOptions{LowerBound: []byte(prefix)})
	defer func() { _ = it.Close() }()
	out := make(map[string]string)
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		if !bytes.HasPrefix(it.Key(), []byte(prefix)) {
			break
		}
		out[string(it.Key())] = string(it.Value())
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterate %q: %v", prefix, err)
	}
	return out
}

// requireNoRefusal fails t when any result is a refusal.
func requireNoRefusal(t *testing.T, results []sm.Result) {
	t.Helper()
	for i, res := range results {
		if msg, ok := protocol.DecodeAppError(res.Data); ok {
			t.Fatalf("result %d refused: %s", i, msg)
		}
	}
}

// logReads answers every read of storeLog the read path offers at size.
func logReads(t *testing.T, n *logNode, size uint64) []any {
	t.Helper()
	return []any{
		n.lookup(t, vlog.HeadRequest{ID: storeLog}),
		n.lookup(t, vlog.GetRequest{ID: storeLog, From: 0, Limit: int(size)}),
		n.lookup(t, vlog.InclusionRequest{ID: storeLog, Index: 1, Size: size}),
		n.lookup(t, vlog.ConsistencyRequest{ID: storeLog, Size1: 3, Size2: size}),
	}
}

// buildMidLog creates storeLog on every node, appends three entries,
// checkpoints them and appends two more, so the head runs past its last
// checkpoint.
func buildMidLog(t *testing.T, nodes ...*logNode) {
	t.Helper()
	p := enc(t)
	for _, n := range nodes {
		requireNoRefusal(t, n.apply(t,
			p(protocol.EncodeVerifiableLogCreate(storeLog, storeLogDescriptor())),
			p(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("a"), []byte("b"), []byte("c")})),
		))
	}
	head := nodes[0].lookup(t, vlog.HeadRequest{ID: storeLog}).(*vlog.Head)
	for _, n := range nodes {
		requireNoRefusal(t, n.apply(t,
			p(protocol.EncodeVerifiableLogCheckpoint(storeLog, 3, head.Root, []byte("signed-3"))),
			p(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("d"), []byte("e")})),
		))
	}
}

// TestStoreVerifiableLog_DispatchesAndAnswersReads pins that both store state
// machines apply OpVerifiableLog onto their log registry and answer the
// vlog read requests from committed storage.
func TestStoreVerifiableLog_DispatchesAndAnswersReads(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			p := enc(t)
			n := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			results := n.apply(t,
				p(protocol.EncodeVerifiableLogCreate(storeLog, storeLogDescriptor())),
				p(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("a"), []byte("b"), []byte("c")})),
				p(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("d")})),
			)
			requireNoRefusal(t, results)
			if results[2].Value != 3 {
				t.Fatalf("second Append = %d, want first index 3", results[2].Value)
			}

			if head := n.lookup(t, vlog.HeadRequest{ID: storeLog}).(*vlog.Head); head.Size != 4 {
				t.Fatalf("head size = %d, want 4", head.Size)
			}
			want, err := vlog.Inclusion(n.reader(), storeLog, 1, 4)
			if err != nil {
				t.Fatalf("Inclusion: %v", err)
			}
			if got := n.lookup(t, vlog.InclusionRequest{ID: storeLog, Index: 1, Size: 4}); !reflect.DeepEqual(got, want) {
				t.Fatalf("inclusion proof = %v, want %v", got, want)
			}
		})
	}
}

// TestStoreVerifiableLog_RefusalVersusHalt pins that a command every replica
// is equally unable to apply is a refusal result, while a sub-type this
// binary does not recognise halts the Update.
func TestStoreVerifiableLog_RefusalVersusHalt(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			p := enc(t)
			n := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			requireNoRefusal(t, n.apply(t, p(protocol.EncodeVerifiableLogCreate(storeLog, storeLogDescriptor()))))

			missing := vlog.LogID{TenantID: storeLog.TenantID, Database: storeLog.Database, Log: "absent"}
			res := n.apply(t, p(protocol.EncodeVerifiableLogAppend(missing, [][]byte{[]byte("x")})))
			if _, kind, ok := protocol.DecodeAppErrorKind(res[0].Data); !ok || kind != protocol.AppErrorInvalid {
				t.Fatalf("append to a missing log = %+v, want an invalid refusal", res[0])
			}

			unknown := p(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("x")}))
			unknown[1] = 0xFF
			var ice *protocol.InvalidCommandError
			if _, err := n.update(unknown); !errors.As(err, &ice) {
				t.Fatalf("Update(unknown sub-type) = %v, want *protocol.InvalidCommandError", err)
			}
		})
	}
}

// TestStoreVerifiableLog_RefusedWithNoRegistry pins that a state machine built
// without a log registry refuses the opcode as a missing capability and
// answers its reads with an error.
func TestStoreVerifiableLog_RefusedWithNoRegistry(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			n := openLogNode(t, kind, t.TempDir(), nil)
			res := n.apply(t, enc(t)(protocol.EncodeVerifiableLogCreate(storeLog, storeLogDescriptor())))
			if _, kind, ok := protocol.DecodeAppErrorKind(res[0].Data); !ok || kind != protocol.AppErrorCapability {
				t.Fatalf("Create without a registry = %+v, want a capability refusal", res[0])
			}
			if _, err := n.sm.Lookup(context.Background(), vlog.HeadRequest{ID: storeLog}); err == nil {
				t.Fatal("Lookup(HeadRequest) without a registry = nil error")
			}
		})
	}
}

// TestStoreAuditAppend_WritesNoMerkleNode pins that OpAuditAppend's apply is
// unchanged: it writes the audit table and no named-log record.
func TestStoreAuditAppend_WritesNoMerkleNode(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			n := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			requireNoRefusal(t, n.apply(t, enc(t)(protocol.EncodeAuditAppend("acme", fixedAuditEvent("acme")))))
			if logs := table(t, n.reader(), vlog.LogTablePrefix); len(logs) != 0 {
				t.Fatalf("OpAuditAppend wrote %d named-log records, want none", len(logs))
			}
			if audits := table(t, n.reader(), vlog.TablePrefix); len(audits) == 0 {
				t.Fatal("OpAuditAppend wrote no audit record")
			}
		})
	}
}

// TestStoreVerifiableLog_SnapshotMidLogMatchesAPeer pins that a replica
// recovered from a snapshot taken while the head runs past its last
// checkpoint serves the same head, entries, proofs and checkpoint as the
// leader, and that its next append writes the same records.
func TestStoreVerifiableLog_SnapshotMidLogMatchesAPeer(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			p := enc(t)
			leader := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			buildMidLog(t, leader)

			var buf bytes.Buffer
			snap, err := leader.sm.PrepareSnapshot()
			if err != nil {
				t.Fatalf("PrepareSnapshot: %v", err)
			}
			if err := leader.sm.SaveSnapshot(ctx, snap, &buf, make(chan struct{})); err != nil {
				t.Fatalf("SaveSnapshot: %v", err)
			}

			// The follower holds a log of its own that the snapshot never
			// names, so a recovery that kept stale in-memory state would
			// still know it.
			follower := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			requireNoRefusal(t, follower.apply(t, p(protocol.EncodeVerifiableLogCreate(
				vlog.LogID{TenantID: "widgets", Database: "certs", Log: "issued"}, storeLogDescriptor()))))
			if err := follower.sm.RecoverFromSnapshot(ctx, &buf, make(chan struct{})); err != nil {
				t.Fatalf("RecoverFromSnapshot: %v", err)
			}

			if got, want := logReads(t, follower, 5), logReads(t, leader, 5); !reflect.DeepEqual(got, want) {
				t.Fatalf("follower reads = %#v, want the leader's %#v", got, want)
			}

			next := p(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("f")}))
			lr, fr := leader.apply(t, next), follower.apply(t, next)
			requireNoRefusal(t, lr)
			requireNoRefusal(t, fr)
			if fr[0].Value != 5 || lr[0].Value != 5 {
				t.Fatalf("next append first index = %d (follower), %d (leader), want 5", fr[0].Value, lr[0].Value)
			}
			if got, want := table(t, follower.reader(), vlog.LogTablePrefix), table(t, leader.reader(), vlog.LogTablePrefix); !reflect.DeepEqual(got, want) {
				t.Fatalf("follower log table diverged from the leader's after the next append: %d records, want %d", len(got), len(want))
			}
		})
	}
}

// TestStoreVerifiableLog_ReopenMatchesAPeer pins that a replica reopened from
// its own directory, with a fresh registry, serves what a peer that never
// restarted serves, and that its next append writes the same records.
func TestStoreVerifiableLog_ReopenMatchesAPeer(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			dir := t.TempDir()
			node := openLogNode(t, kind, dir, vlog.NewChain())
			peer := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			buildMidLog(t, node, peer)

			if err := node.sm.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			reopened := openLogNode(t, kind, dir, vlog.NewChain())
			reopened.next = node.next

			if got, want := logReads(t, reopened, 5), logReads(t, peer, 5); !reflect.DeepEqual(got, want) {
				t.Fatalf("reopened reads = %#v, want the peer's %#v", got, want)
			}

			next := enc(t)(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("f")}))
			requireNoRefusal(t, reopened.apply(t, next))
			requireNoRefusal(t, peer.apply(t, next))
			if got, want := table(t, reopened.reader(), vlog.LogTablePrefix), table(t, peer.reader(), vlog.LogTablePrefix); !reflect.DeepEqual(got, want) {
				t.Fatalf("reopened log table diverged from the peer's after the next append: %d records, want %d", len(got), len(want))
			}
		})
	}
}

// TestStoreVerifiableLog_FailedUpdateRestoresTheNamedLogs pins the resync: an
// Update that halts after an append staged into its batch commits nothing,
// so the registry must not keep that append's head, or the next append would
// assign indexes past entries no replica holds.
func TestStoreVerifiableLog_FailedUpdateRestoresTheNamedLogs(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			p := enc(t)
			n := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			peer := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			for _, node := range []*logNode{n, peer} {
				requireNoRefusal(t, node.apply(t,
					p(protocol.EncodeVerifiableLogCreate(storeLog, storeLogDescriptor())),
					p(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("a")})),
				))
			}

			unknown := p(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("x")}))
			unknown[1] = 0xFF
			if _, err := n.update(p(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("b")})), unknown); err == nil {
				t.Fatal("Update with an unknown sub-type = nil error, want a halt")
			}

			next := p(protocol.EncodeVerifiableLogAppend(storeLog, [][]byte{[]byte("c")}))
			res := n.apply(t, next)
			requireNoRefusal(t, res)
			if res[0].Value != 1 {
				t.Fatalf("next append first index = %d, want 1: the failed Update's append outlived it", res[0].Value)
			}
			requireNoRefusal(t, peer.apply(t, next))
			if got, want := table(t, n.reader(), vlog.LogTablePrefix), table(t, peer.reader(), vlog.LogTablePrefix); !reflect.DeepEqual(got, want) {
				t.Fatalf("log table diverged from the peer's after a failed Update: %d records, want %d", len(got), len(want))
			}
		})
	}
}

// TestLogsResyncError_CarriesBothFailures pins that a failed resync reports,
// and unwraps to, both the apply fault and the restore fault.
func TestLogsResyncError_CarriesBothFailures(t *testing.T) {
	applyErr, restoreErr := errors.New("disk full"), errors.New("corrupt head")
	err := error(&store.LogsResyncError{ApplyErr: applyErr, RestoreErr: restoreErr})
	if !errors.Is(err, applyErr) || !errors.Is(err, restoreErr) {
		t.Fatalf("%v does not unwrap to both failures", err)
	}
	for _, part := range []string{"disk full", "corrupt head"} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("Error() = %q, want it to name %q", err.Error(), part)
		}
	}
}
