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
	"encoding/json"
	"errors"
	"fmt"
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

// TestStoreAuditInstance_FamiliesAgree applies the same committed entries --
// OpAuditAppend before the switch, OpVerifiableLog appends to the audit
// instance after it, and a prune -- to both state-machine families, and
// requires identical results and identical audit and log tables.
func TestStoreAuditInstance_FamiliesAgree(t *testing.T) {
	p := enc(t)
	event := func(i int) []byte {
		ev := fixedAuditEvent("acme")
		ev.Action = fmt.Sprintf("action-%d", i)
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var entries [][]byte
	for i := range 3 {
		ev := fixedAuditEvent("acme")
		ev.Action = fmt.Sprintf("action-%d", i)
		entries = append(entries, p(protocol.EncodeAuditAppend("acme", ev)))
	}
	for i := 3; i < 8; i++ {
		entries = append(entries, p(protocol.EncodeVerifiableLogAuditAppend(vlog.AuditLogID("acme"), [][]byte{event(i)})))
	}
	entries = append(entries, p(protocol.EncodeAuditPrune("acme", 6)))

	type state struct {
		values      []uint64
		audit, logs map[string]string
		root        []byte
		treeSize    uint64
		seq, lo     uint64
		digest      [32]byte
	}
	var states []state
	for _, kind := range auditSMKinds {
		logs := vlog.NewChain()
		n := openLogNode(t, kind, t.TempDir(), logs)
		var s state
		for _, e := range entries {
			res := n.apply(t, e)
			requireNoRefusal(t, res)
			s.values = append(s.values, res[0].Value)
		}
		s.audit, s.logs = table(t, n.reader(), vlog.TablePrefix), table(t, n.reader(), vlog.LogTablePrefix)
		head := n.lookup(t, vlog.HeadRequest{ID: vlog.AuditLogID("acme")}).(*vlog.Head)
		s.root, s.treeSize = head.Root, head.Size
		s.seq, s.lo, s.digest = logs.Head("acme")
		states = append(states, s)
	}
	if states[0].treeSize != 5 || len(states[0].root) == 0 {
		t.Fatalf("audit instance head = size %d, root %x; want 5 leaves and a root", states[0].treeSize, states[0].root)
	}
	if states[0].seq != 8 || states[0].digest == ([32]byte{}) {
		t.Fatalf("linear head = seq %d, digest %x; want seq 8 and a digest", states[0].seq, states[0].digest)
	}
	if want := []uint64{1, 2, 3, 4, 5, 6, 7, 8, 5}; !reflect.DeepEqual(states[0].values, want) {
		t.Fatalf("results = %v, want %v", states[0].values, want)
	}
	if len(states[0].logs) == 0 {
		t.Fatal("the audit instance wrote no tree")
	}
	for i := 1; i < len(states); i++ {
		if !reflect.DeepEqual(states[i], states[0]) {
			t.Fatalf("%s's state differs from %s's", auditSMKinds[i].name, auditSMKinds[0].name)
		}
	}
}

// TestStoreAuditInstance_LogSurfaceExpireIsRefused pins that an
// OpVerifiableLog ExpireBodies naming the audit instance is a refusal on both
// families and changes nothing: the instance's retention is audit Prune.
func TestStoreAuditInstance_LogSurfaceExpireIsRefused(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			p := enc(t)
			n := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			body, err := json.Marshal(fixedAuditEvent("acme"))
			if err != nil {
				t.Fatal(err)
			}
			requireNoRefusal(t, n.apply(t, p(protocol.EncodeVerifiableLogAuditAppend(vlog.AuditLogID("acme"), [][]byte{body}))))
			before := table(t, n.reader(), vlog.TablePrefix)

			res := n.apply(t, p(protocol.EncodeVerifiableLogExpireBodies(vlog.AuditLogID("acme"), 1)))
			if reason, _, _, ok := protocol.DecodeLogRefusal(res[0].Data); !ok || reason != protocol.LogRefusalExpireNotAllowed {
				t.Fatalf("ExpireBodies on the audit instance = %+v, want an expire-not-allowed refusal", res[0])
			}
			if after := table(t, n.reader(), vlog.TablePrefix); !reflect.DeepEqual(after, before) {
				t.Fatal("a refused ExpireBodies changed the audit table")
			}
		})
	}
}

// TestStoreAuditInstance_GenericAppendIsRefused pins that the generic Append
// sub-type never reaches the audit instance: a client's entry chained as an
// audit event would be a forged event, so the apply refuses the audit name
// with ReservedLogError on every replica and changes nothing. Only the
// AuditAppend sub-type, which the replicated audit logger alone proposes,
// appends to the instance; it in turn refuses any other log.
func TestStoreAuditInstance_GenericAppendIsRefused(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			p := enc(t)
			n := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			body, err := json.Marshal(fixedAuditEvent("acme"))
			if err != nil {
				t.Fatal(err)
			}
			requireNoRefusal(t, n.apply(t, p(protocol.EncodeVerifiableLogAuditAppend(vlog.AuditLogID("acme"), [][]byte{body}))))
			auditBefore, logsBefore := table(t, n.reader(), vlog.TablePrefix), table(t, n.reader(), vlog.LogTablePrefix)

			forged, err := json.Marshal(fixedAuditEvent("acme"))
			if err != nil {
				t.Fatal(err)
			}
			res := n.apply(t, p(protocol.EncodeVerifiableLogAppend(vlog.AuditLogID("acme"), [][]byte{forged})))
			msg, errKind, ok := protocol.DecodeAppErrorKind(res[0].Data)
			if want := (&vlog.ReservedLogError{ID: vlog.AuditLogID("acme")}).Error(); !ok || errKind != protocol.AppErrorInvalid || msg != want {
				t.Fatalf("generic append to the audit instance = %+v, want an invalid refusal %q", res[0], want)
			}
			res = n.apply(t, p(protocol.EncodeVerifiableLogAuditAppend(storeLog, [][]byte{forged})))
			if msg, _, ok := protocol.DecodeAppErrorKind(res[0].Data); !ok || msg != (&vlog.NotAuditLogError{ID: storeLog}).Error() {
				t.Fatalf("audit append to %v = %+v, want a not-audit-log refusal", storeLog, res[0])
			}
			if after := table(t, n.reader(), vlog.TablePrefix); !reflect.DeepEqual(after, auditBefore) {
				t.Fatal("a refused append changed the audit table")
			}
			if after := table(t, n.reader(), vlog.LogTablePrefix); !reflect.DeepEqual(after, logsBefore) {
				t.Fatal("a refused append changed the named-log table")
			}
		})
	}
}

// TestStoreAuditInstance_MixedVersionReplay pins the rolling upgrade: until
// featuregate.VerifiableLogAudit is enabled the replicated audit logger
// proposes OpAuditAppend (with VerifiableLog on or off), which a replica
// built before the audit instance applies by extending the linear chain
// alone and writing no named-log record. A replica of this build applying
// the same committed entries reaches that state byte for byte, on every
// state machine, so a mixed group cannot diverge on an audit event.
func TestStoreAuditInstance_MixedVersionReplay(t *testing.T) {
	p := enc(t)
	var entries [][]byte
	for i := range 4 {
		ev := fixedAuditEvent("acme")
		ev.Action = fmt.Sprintf("action-%d", i)
		entries = append(entries, p(protocol.EncodeAuditAppend("acme", ev)))
	}
	entries = append(entries, p(protocol.EncodeAuditPrune("acme", 2)))

	type state struct {
		values []uint64
		audit  map[string]string
		logs   int
	}
	var states []state
	for _, kind := range auditSMKinds {
		n := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
		var s state
		for _, e := range entries {
			res := n.apply(t, e)
			requireNoRefusal(t, res)
			s.values = append(s.values, res[0].Value)
		}
		s.audit, s.logs = table(t, n.reader(), vlog.TablePrefix), len(table(t, n.reader(), vlog.LogTablePrefix))
		states = append(states, s)
	}
	// The pre-instance apply: sequences 1..4, the prune's count, and no
	// named-log record.
	if want := []uint64{1, 2, 3, 4, 1}; !reflect.DeepEqual(states[0].values, want) {
		t.Fatalf("results = %v, want %v", states[0].values, want)
	}
	if states[0].logs != 0 {
		t.Fatalf("OpAuditAppend wrote %d named-log records; a pre-instance replica writes none", states[0].logs)
	}
	for i := 1; i < len(states); i++ {
		if !reflect.DeepEqual(states[i], states[0]) {
			t.Fatalf("%s's state differs from %s's", auditSMKinds[i].name, auditSMKinds[0].name)
		}
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

// TestStoreVerifiableLog_SigningKeySurvivesSnapshot covers the head-signing
// key a keyed Create writes: it is part of the log's table, so a snapshot
// carries it to a follower, which reads the same key back.
func TestStoreVerifiableLog_SigningKeySurvivesSnapshot(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			p := enc(t)
			key, err := vlog.GenerateSigningKey(vlog.SignatureECDSAP256SHA256, vlog.FirstKeyVersion)
			if err != nil {
				t.Fatalf("GenerateSigningKey: %v", err)
			}
			desc := storeLogDescriptor()
			signer, _, err := key.Signer()
			if err != nil {
				t.Fatalf("Signer: %v", err)
			}
			hk, err := vlog.NewHeadKey(key.Version, vlog.KeyRef(key.Version), signer.Public())
			if err != nil {
				t.Fatalf("NewHeadKey: %v", err)
			}
			desc.Keys = []vlog.HeadKey{hk}
			leader := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			requireNoRefusal(t, leader.apply(t, p(protocol.EncodeVerifiableLogCreateWithKey(storeLog, desc, key))))

			var buf bytes.Buffer
			snap, err := leader.sm.PrepareSnapshot()
			if err != nil {
				t.Fatalf("PrepareSnapshot: %v", err)
			}
			if err := leader.sm.SaveSnapshot(ctx, snap, &buf, make(chan struct{})); err != nil {
				t.Fatalf("SaveSnapshot: %v", err)
			}
			follower := openLogNode(t, kind, t.TempDir(), vlog.NewChain())
			if err := follower.sm.RecoverFromSnapshot(ctx, &buf, make(chan struct{})); err != nil {
				t.Fatalf("RecoverFromSnapshot: %v", err)
			}

			got, err := vlog.ReadSigningKey(follower.reader(), storeLog, key.Version)
			if err != nil || !reflect.DeepEqual(*got, key) {
				t.Fatalf("follower ReadSigningKey = %+v, %v; want the leader's key", got, err)
			}
			d, err := vlog.ReadDescriptor(follower.reader(), storeLog)
			if err != nil || !reflect.DeepEqual(d.Keys, desc.Keys) {
				t.Fatalf("follower ReadDescriptor = %+v, %v; want keys %+v", d, err, desc.Keys)
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
