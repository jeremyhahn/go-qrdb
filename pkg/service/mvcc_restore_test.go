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
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// ---------------------------------------------------------------------------
// In-memory MVCC emulator
//
// RestoreToRevision is a read-diff-write algorithm, so testing it against a
// host that returns canned values would only prove the code calls the methods
// it calls. This emulator reproduces the parts of pkg/mvcc's behaviour the
// algorithm actually depends on, including the two that matter most:
//
//   - Historical reads below CompactedRevision fail closed.
//   - Range enumerates the LIVE key set and then resolves each key at the
//     target revision, so a key deleted after the target revision is absent
//     from a historical range. This is the property that makes the revision
//     index necessary, and the emulator reproduces it faithfully so the tests
//     prove the index is what recovers those keys.
//   - ScanChanges reads the revision index and therefore DOES report keys
//     deleted in the window.
// ---------------------------------------------------------------------------

// restoreVersion is one version of one key in the emulated history.
type restoreVersion struct {
	rev     int64
	value   []byte
	deleted bool
}

// restoreStoreMock is an ordered, versioned key-value history that answers the
// MVCC queries the service issues and applies the writes it proposes.
type restoreStoreMock struct {
	revision  int64
	compacted int64
	history   map[string][]restoreVersion

	// writes records every mutation the service proposed, in order, as
	// "put:key=value" or "del:key". A dry run must leave it empty.
	writes []string

	// proposeErr injects a write failure for one key.
	proposeErr map[string]error

	// scanErr injects a revision-index scan failure.
	scanErr error

	// beforeApply runs once, immediately before the first proposed write, so
	// a test can mutate the store between the diff and the apply and exercise
	// the compare-and-swap guard.
	beforeApply func()
}

func newRestoreStoreMock() *restoreStoreMock {
	return &restoreStoreMock{
		history:    make(map[string][]restoreVersion),
		proposeErr: make(map[string]error),
	}
}

// put appends a new version of key and returns the revision it was written at.
func (s *restoreStoreMock) put(key, value string) int64 {
	s.revision++
	s.history[key] = append(s.history[key], restoreVersion{rev: s.revision, value: []byte(value)})
	return s.revision
}

// del appends a tombstone for key and returns the revision it was written at.
func (s *restoreStoreMock) del(key string) int64 {
	s.revision++
	s.history[key] = append(s.history[key], restoreVersion{rev: s.revision, deleted: true})
	return s.revision
}

// at resolves key as of revision, mirroring mvcc.Store.GetAtRevision: the
// compaction floor is checked first, a missing history yields a
// revision-not-found error, and a tombstone yields a key-not-found error.
func (s *restoreStoreMock) at(key string, revision int64) (*KeyValue, error) {
	if revision > 0 && revision <= s.compacted {
		return nil, &mockRevisionCompactedError{requested: revision, compacted: s.compacted}
	}
	target := revision
	if target == 0 {
		target = s.revision
	}

	var found *restoreVersion
	for i := range s.history[key] {
		v := s.history[key][i]
		if v.rev <= target {
			found = &s.history[key][i]
		}
	}
	if found == nil {
		return nil, &mockRevisionNotFoundError{key: key, revision: revision}
	}
	if found.deleted {
		return nil, &mockKeyNotFoundError{key: key}
	}
	return &KeyValue{Key: []byte(key), Value: found.value, ModRevision: found.rev}, nil
}

// live returns the sorted set of keys whose newest version is not a tombstone.
// It is the set mvcc.Store.Range scans (the metadata namespace).
func (s *restoreStoreMock) live() []string {
	out := make([]string, 0, len(s.history))
	for key, versions := range s.history {
		if len(versions) > 0 && !versions[len(versions)-1].deleted {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// read answers the MVCC queries MVCCKVService issues.
func (s *restoreStoreMock) read(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
	switch q := query.(type) {
	case MVCCRevisionQuery:
		return s.revision, nil

	case MVCCGetQuery:
		kv, err := s.at(string(q.Key), 0)
		if err != nil {
			// A latest-revision read reports absence as key-not-found
			// whether the key was deleted or never existed.
			return nil, &mockKeyNotFoundError{key: string(q.Key)}
		}
		return kv, nil

	case MVCCGetAtRevisionQuery:
		return s.at(string(q.Key), q.Revision)

	case MVCCRangeQuery:
		return s.rangeQuery(q)

	case MVCCScanChangesQuery:
		return s.scanChangesQuery(q)
	}
	return nil, errors.New("restoreStoreMock: unsupported query")
}

// scanChangesQuery mirrors the engine's revision-index scan: every key with a
// version in the inclusive window, deletes included, deduplicated and sorted.
func (s *restoreStoreMock) scanChangesQuery(q MVCCScanChangesQuery) (interface{}, error) {
	if s.scanErr != nil {
		return nil, s.scanErr
	}

	result := &MVCCScanChangesResult{Revision: s.revision}
	prefix := string(q.Prefix)

	var keys []string
	for key, versions := range s.history {
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		for _, v := range versions {
			if v.rev >= q.StartRevision && v.rev <= q.EndRevision {
				keys = append(keys, key)
				break
			}
		}
	}
	sort.Strings(keys)

	if q.Limit > 0 && int64(len(keys)) > q.Limit {
		keys = keys[:q.Limit]
		result.Truncated = true
	}
	result.Keys = keys
	return result, nil
}

// rangeQuery mirrors mvcc.Store.Range: refuse below the compaction floor,
// then resolve every live key in [Start, End) at the target revision.
func (s *restoreStoreMock) rangeQuery(q MVCCRangeQuery) (interface{}, error) {
	if q.Revision > 0 && q.Revision <= s.compacted {
		return nil, &mockRevisionCompactedError{requested: q.Revision, compacted: s.compacted}
	}

	result := &MVCCRangeResult{Revision: s.revision}
	for _, key := range s.live() {
		if key < string(q.Start) || (q.End != nil && key >= string(q.End)) {
			continue
		}
		kv, err := s.at(key, q.Revision)
		if err != nil {
			continue
		}
		result.KeyValues = append(result.KeyValues, kv)
		result.Count++
		if q.Limit > 0 && result.Count >= q.Limit {
			break
		}
	}
	return result, nil
}

// propose applies a proposed MVCC write to the history and records it.
func (s *restoreStoreMock) propose(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
	if s.beforeApply != nil {
		hook := s.beforeApply
		s.beforeApply = nil
		hook()
	}

	switch cmd[0] {
	case protocol.OpTransaction:
		return s.proposeTransaction(cmd[1:])

	case protocol.OpMVCCPut:
		key, value, _, err := protocol.DecodeMVCCPut(cmd[1:])
		if err != nil {
			return sm.Result{}, err
		}
		if injected, ok := s.proposeErr[string(key)]; ok {
			return sm.Result{}, injected
		}
		s.writes = append(s.writes, "put:"+string(key)+"="+string(value))
		return sm.Result{Value: uint64(s.put(string(key), string(value)))}, nil

	case protocol.OpMVCCDelete:
		key, err := protocol.DecodeMVCCDelete(cmd[1:])
		if err != nil {
			return sm.Result{}, err
		}
		if injected, ok := s.proposeErr[string(key)]; ok {
			return sm.Result{}, injected
		}
		s.writes = append(s.writes, "del:"+string(key))
		return sm.Result{Value: uint64(s.del(string(key)))}, nil
	}
	return sm.Result{}, errors.New("restoreStoreMock: unsupported command")
}

// proposeTransaction evaluates a transaction's preconditions against the
// current state and applies its success operations only when all hold,
// reproducing the state machine's atomic compare-and-swap. The succeeded flag
// is returned in Data[0], matching what TransactionService parses.
func (s *restoreStoreMock) proposeTransaction(payload []byte) (sm.Result, error) {
	txn, err := protocol.DecodeTransaction(payload)
	if err != nil {
		return sm.Result{}, err
	}

	succeeded := true
	for _, c := range txn.Compares {
		// Only the mod-revision equality guard the restore issues is
		// modelled; anything else is a test bug worth failing loudly on.
		if c.Target != byte(CompareModRevision) || c.Result != byte(CompareEqual) {
			return sm.Result{}, errors.New("restoreStoreMock: unsupported compare")
		}
		var actual int64
		if kv, kerr := s.at(string(c.Key), 0); kerr == nil {
			actual = kv.ModRevision
		}
		if actual != c.ModRevision {
			succeeded = false
			break
		}
	}

	data := []byte{0}
	if succeeded {
		data[0] = 1
		for _, op := range txn.SuccessOps {
			key := string(op.Key)
			if injected, ok := s.proposeErr[key]; ok {
				return sm.Result{}, injected
			}
			// Transaction ops carry the protocol opcode, not the service
			// OpType enum (see service.opTypeMap).
			switch op.Type {
			case protocol.OpMVCCPut:
				s.writes = append(s.writes, "put:"+key+"="+string(op.Value))
				s.put(key, string(op.Value))
			case protocol.OpMVCCDelete:
				s.writes = append(s.writes, "del:"+key)
				s.del(key)
			default:
				return sm.Result{}, errors.New("restoreStoreMock: unsupported txn op")
			}
		}
	}

	return sm.Result{Value: uint64(s.revision), Data: data}, nil
}

// service returns an MVCCKVService backed by this emulated store.
func (s *restoreStoreMock) service() *MVCCKVService {
	return &MVCCKVService{
		host:    &mockHost{syncReadFn: s.read, syncProposeFn: s.propose},
		shardID: 1,
	}
}

// planOf renders a result's plan as sorted "action key" strings for compact
// comparison.
func planOf(result *RestoreRevisionResult) []string {
	out := make([]string, 0, len(result.Changes))
	for _, c := range result.Changes {
		out = append(out, string(c.Action)+" "+c.Key)
	}
	return out
}

func assertStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Happy paths
// ---------------------------------------------------------------------------

func TestMVCCKVService_RestoreToRevision_RevertsChangedValue(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}

	assertStrings(t, "plan", planOf(result), []string{"revert app/a"})
	assertStrings(t, "writes", store.writes, []string{"put:app/a=v1"})
	if result.Applied != 1 || result.Skipped != 0 {
		t.Fatalf("applied=%d skipped=%d, want 1/0", result.Applied, result.Skipped)
	}

	kv, err := store.at("app/a", 0)
	if err != nil || string(kv.Value) != "v1" {
		t.Fatalf("post-restore value: %v, %v", kv, err)
	}
}

func TestMVCCKVService_RestoreToRevision_RevertRecordsRevisions(t *testing.T) {
	store := newRestoreStoreMock()
	oldRev := store.put("app/a", "v1")
	target := store.revision
	newRev := store.put("app/a", "v2")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Keys:     []string{"app/a"},
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	change := result.Changes[0]
	if change.FromRevision != newRev || change.ToRevision != oldRev {
		t.Fatalf("from=%d to=%d, want from=%d to=%d",
			change.FromRevision, change.ToRevision, newRev, oldRev)
	}
	if change.ValueBytes != int64(len("v1")) {
		t.Fatalf("ValueBytes = %d, want %d", change.ValueBytes, len("v1"))
	}
}

func TestMVCCKVService_RestoreToRevision_RecreatesDeletedKey(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.del("app/a")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Keys:     []string{"app/a"},
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}

	assertStrings(t, "plan", planOf(result), []string{"recreate app/a"})
	assertStrings(t, "writes", store.writes, []string{"put:app/a=v1"})
	if result.Changes[0].FromRevision != 0 {
		t.Fatalf("FromRevision = %d, want 0 for a recreate", result.Changes[0].FromRevision)
	}
}

func TestMVCCKVService_RestoreToRevision_DeletesKeyCreatedAfterTarget(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/new", "v1")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}

	assertStrings(t, "plan", planOf(result), []string{"delete app/new"})
	assertStrings(t, "writes", store.writes, []string{"del:app/new"})
	if result.Changes[0].ValueBytes != 0 {
		t.Fatalf("ValueBytes = %d, want 0 for a delete", result.Changes[0].ValueBytes)
	}
	if result.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1 (app/a is unchanged)", result.Skipped)
	}
}

func TestMVCCKVService_RestoreToRevision_SkipsIdenticalKeys(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	store.put("app/b", "v1")
	target := store.revision

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if len(result.Changes) != 0 || result.Applied != 0 {
		t.Fatalf("expected an empty plan, got %v (applied=%d)", planOf(result), result.Applied)
	}
	if result.Skipped != 2 {
		t.Fatalf("Skipped = %d, want 2", result.Skipped)
	}
	if len(store.writes) != 0 {
		t.Fatalf("expected no writes, got %v", store.writes)
	}
}

func TestMVCCKVService_RestoreToRevision_KeysScopeSkipsKeyAbsentInBothStates(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Keys:     []string{"app/never-existed"},
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if len(result.Changes) != 0 || result.Skipped != 1 {
		t.Fatalf("plan=%v skipped=%d, want empty/1", planOf(result), result.Skipped)
	}
}

func TestMVCCKVService_RestoreToRevision_MixedPlanIsOrderedByKey(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	store.put("app/c", "v1")
	target := store.revision
	store.put("app/a", "v2") // revert
	store.put("app/b", "v1") // delete
	store.put("app/c", "v1") // identical value rewritten -> skip

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	assertStrings(t, "plan", planOf(result), []string{"revert app/a", "delete app/b"})
	assertStrings(t, "writes", store.writes, []string{"put:app/a=v1", "del:app/b"})
	if result.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", result.Skipped)
	}
}

func TestMVCCKVService_RestoreToRevision_KeysScopeCollapsesDuplicates(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Keys:     []string{"app/a", "app/a", "app/a"},
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	assertStrings(t, "plan", planOf(result), []string{"revert app/a"})
	assertStrings(t, "writes", store.writes, []string{"put:app/a=v1"})
}

func TestMVCCKVService_RestoreToRevision_PrefixScopeIsBoundedToPrefix(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	store.put("other/a", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.put("other/a", "v2")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	assertStrings(t, "plan", planOf(result), []string{"revert app/a"})

	kv, err := store.at("other/a", 0)
	if err != nil || string(kv.Value) != "v2" {
		t.Fatalf("out-of-scope key was modified: %v, %v", kv, err)
	}
}

// ---------------------------------------------------------------------------
// Dry run
// ---------------------------------------------------------------------------

func TestMVCCKVService_RestoreToRevision_DryRunWritesNothing(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.put("app/new", "v1")
	revBefore := store.revision

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}

	if !result.DryRun {
		t.Fatal("result.DryRun = false, want true")
	}
	if result.Applied != 0 {
		t.Fatalf("Applied = %d, want 0 on a dry run", result.Applied)
	}
	if len(store.writes) != 0 {
		t.Fatalf("dry run proposed writes: %v", store.writes)
	}
	if store.revision != revBefore {
		t.Fatalf("dry run advanced the revision: %d -> %d", revBefore, store.revision)
	}
	kv, err := store.at("app/a", 0)
	if err != nil || string(kv.Value) != "v2" {
		t.Fatalf("dry run changed stored state: %v, %v", kv, err)
	}
}

func TestMVCCKVService_RestoreToRevision_DryRunPlanMatchesAppliedPlan(t *testing.T) {
	build := func() (*restoreStoreMock, int64) {
		store := newRestoreStoreMock()
		store.put("app/a", "v1")
		store.put("app/gone", "v1")
		target := store.revision
		store.put("app/a", "v2")
		store.put("app/new", "v1")
		store.del("app/gone")
		return store, target
	}

	dryStore, target := build()
	dry, err := dryStore.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target, Prefix: "app/", DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}

	applyStore, target2 := build()
	applied, err := applyStore.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target2, Prefix: "app/",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	assertStrings(t, "plan", planOf(dry), planOf(applied))
	if dry.Skipped != applied.Skipped {
		t.Fatalf("skipped: dry=%d applied=%d", dry.Skipped, applied.Skipped)
	}
	if applied.Applied != len(applied.Changes) {
		t.Fatalf("Applied = %d, want %d", applied.Applied, len(applied.Changes))
	}
}

// ---------------------------------------------------------------------------
// Fail-closed revision validation
// ---------------------------------------------------------------------------

func TestMVCCKVService_RestoreToRevision_RejectsCompactedRevision_PrefixScope(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.compacted = target // the target revision is now at the floor

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	var compacted *RevisionCompactedError
	if !errors.As(err, &compacted) {
		t.Fatalf("expected *RevisionCompactedError, got %#v", err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused restore wrote: %v", store.writes)
	}
}

func TestMVCCKVService_RestoreToRevision_RejectsCompactedRevision_KeysScope(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.compacted = target + 1

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Keys:     []string{"app/a"},
	})
	var compacted *RevisionCompactedError
	if !errors.As(err, &compacted) {
		t.Fatalf("expected *RevisionCompactedError, got %#v", err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused restore wrote: %v", store.writes)
	}
}

func TestMVCCKVService_RestoreToRevision_RejectsFutureRevision(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: store.revision + 5,
		Prefix:   "app/",
	})
	var future *RestoreRevisionFutureError
	if !errors.As(err, &future) {
		t.Fatalf("expected *RestoreRevisionFutureError, got %#v", err)
	}
	if future.Requested != store.revision+5 || future.Current != store.revision {
		t.Fatalf("requested=%d current=%d", future.Requested, future.Current)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused restore wrote: %v", store.writes)
	}
}

func TestMVCCKVService_RestoreToRevision_RejectsNonPositiveRevision(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")

	for _, rev := range []int64{0, -1, -100} {
		_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
			Revision: rev,
			Prefix:   "app/",
		})
		var invalid *RestoreRevisionInvalidError
		if !errors.As(err, &invalid) {
			t.Fatalf("revision %d: expected *RestoreRevisionInvalidError, got %#v", rev, err)
		}
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused restore wrote: %v", store.writes)
	}
}

func TestMVCCKVService_RestoreToRevision_RejectsNilRequest(t *testing.T) {
	store := newRestoreStoreMock()
	_, err := store.service().RestoreToRevision(context.Background(), nil)
	var nilReq *RestoreNilRequestError
	if !errors.As(err, &nilReq) {
		t.Fatalf("expected *RestoreNilRequestError, got %#v", err)
	}
}

// ---------------------------------------------------------------------------
// SECURITY: scope must be explicit
// ---------------------------------------------------------------------------

func TestMVCCKVService_RestoreToRevision_RejectsEmptyScope(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
	})
	var empty *RestoreScopeEmptyError
	if !errors.As(err, &empty) {
		t.Fatalf("expected *RestoreScopeEmptyError, got %#v", err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("an unscoped restore wrote: %v", store.writes)
	}
}

func TestMVCCKVService_RestoreToRevision_RejectsAmbiguousScope(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
		Keys:     []string{"app/a"},
	})
	var ambiguous *RestoreScopeAmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("expected *RestoreScopeAmbiguousError, got %#v", err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("an ambiguous restore wrote: %v", store.writes)
	}
}

func TestMVCCKVService_RestoreToRevision_RejectsEmptyKeyInList(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: store.revision,
		Keys:     []string{"app/a", ""},
	})
	var emptyKey *RestoreEmptyKeyError
	if !errors.As(err, &emptyKey) {
		t.Fatalf("expected *RestoreEmptyKeyError, got %#v", err)
	}
	if emptyKey.Index != 1 {
		t.Fatalf("Index = %d, want 1", emptyKey.Index)
	}
}

// ---------------------------------------------------------------------------
// SECURITY: the internal key namespace is unreachable
// ---------------------------------------------------------------------------

// TestMVCCKVService_RestoreToRevision_RejectsSystemPrefix pins the guard
// against the specific escalation this operation could otherwise enable: the
// read paths in pkg/engine let an explicit "__sys:" prefix bypass their
// internal-key filter, and a restore with the same bypass would let a
// data-plane verb roll back a permission revocation.
func TestMVCCKVService_RestoreToRevision_RejectsSystemPrefix(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("__sys:roles/admin", "full")
	target := store.revision
	store.put("__sys:roles/admin", "revoked")

	for _, prefix := range []string{"__sys:", "__sys:roles/", "__sys:users/", "__sys:roles/admin"} {
		_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
			Revision: target,
			Prefix:   prefix,
		})
		var internal *RestoreInternalKeyError
		if !errors.As(err, &internal) {
			t.Fatalf("prefix %q: expected *RestoreInternalKeyError, got %#v", prefix, err)
		}
		if internal.Prefix != prefix {
			t.Fatalf("prefix %q: error carried %q", prefix, internal.Prefix)
		}
	}

	if len(store.writes) != 0 {
		t.Fatalf("a refused internal-namespace restore wrote: %v", store.writes)
	}
	kv, err := store.at("__sys:roles/admin", 0)
	if err != nil || string(kv.Value) != "revoked" {
		t.Fatalf("the revocation was undone: %v, %v", kv, err)
	}
}

func TestMVCCKVService_RestoreToRevision_RejectsInternalPrefixes(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision

	for _, prefix := range []string{"__sys:", "ts:", "idx:metric:", "idx:tag:", "retention:"} {
		_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
			Revision: target,
			Prefix:   prefix,
		})
		var internal *RestoreInternalKeyError
		if !errors.As(err, &internal) {
			t.Fatalf("prefix %q: expected *RestoreInternalKeyError, got %#v", prefix, err)
		}
	}
}

func TestMVCCKVService_RestoreToRevision_RejectsInternalKeysInList(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision

	internalKeys := []string{
		"__sys:roles/admin",
		"__sys:users/root",
		"ts:cpu/1700000000",
		"idx:metric:cpu",
		"idx:tag:host=web1",
		"retention:cpu",
	}
	for _, key := range internalKeys {
		_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
			Revision: target,
			Keys:     []string{key},
		})
		var internal *RestoreInternalKeyError
		if !errors.As(err, &internal) {
			t.Fatalf("key %q: expected *RestoreInternalKeyError, got %#v", key, err)
		}
		if internal.Key != key {
			t.Fatalf("key %q: error carried %q", key, internal.Key)
		}
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused internal-key restore wrote: %v", store.writes)
	}
}

// TestMVCCKVService_RestoreToRevision_RejectsInternalKeySmuggledInList proves
// the guard rejects the WHOLE request rather than dropping the offending key
// and restoring the rest, so an internal key cannot be smuggled in behind a
// legitimate one.
func TestMVCCKVService_RestoreToRevision_RejectsInternalKeySmuggledInList(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	store.put("__sys:roles/admin", "full")
	target := store.revision
	store.put("app/a", "v2")
	store.put("__sys:roles/admin", "revoked")

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Keys:     []string{"app/a", "__sys:roles/admin"},
	})
	var internal *RestoreInternalKeyError
	if !errors.As(err, &internal) {
		t.Fatalf("expected *RestoreInternalKeyError, got %#v", err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("the request was partially applied: %v", store.writes)
	}
}

// TestMVCCKVService_RestoreToRevision_FiltersInternalKeysFromPrefixRange is the
// second half of the internal-key guard. "_" and "i" are legitimate user
// prefixes, but their ranges span "__sys:…" and "idx:metric:…" respectively,
// and the MVCC range query applies no internal-key filter of its own.
func TestMVCCKVService_RestoreToRevision_FiltersInternalKeysFromPrefixRange(t *testing.T) {
	cases := []struct {
		name        string
		prefix      string
		userKey     string
		internalKey string
	}{
		{"underscore spans __sys:", "_", "_user/a", "__sys:roles/admin"},
		{"i spans idx:metric:", "i", "items/a", "idx:metric:cpu"},
		{"i spans idx:tag:", "i", "items/a", "idx:tag:host=web1"},
		{"r spans retention:", "r", "reports/a", "retention:cpu"},
		{"t spans ts:", "t", "tasks/a", "ts:cpu/1700000000"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newRestoreStoreMock()
			store.put(tc.userKey, "v1")
			store.put(tc.internalKey, "internal-v1")
			target := store.revision
			store.put(tc.userKey, "v2")
			store.put(tc.internalKey, "internal-v2")

			result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
				Revision: target,
				Prefix:   tc.prefix,
			})
			if err != nil {
				t.Fatalf("RestoreToRevision: %v", err)
			}

			assertStrings(t, "plan", planOf(result), []string{"revert " + tc.userKey})
			assertStrings(t, "writes", store.writes, []string{"put:" + tc.userKey + "=v1"})

			kv, err := store.at(tc.internalKey, 0)
			if err != nil || string(kv.Value) != "internal-v2" {
				t.Fatalf("internal key %q was restored: %v, %v", tc.internalKey, kv, err)
			}
		})
	}
}

// TestMVCCKVService_RestoreToRevision_DoesNotDeleteInternalKeyCreatedAfterTarget
// covers the delete side of the range filter: an internal key created after
// the target revision must not be swept away as "created after R".
func TestMVCCKVService_RestoreToRevision_DoesNotDeleteInternalKeyCreatedAfterTarget(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("items/a", "v1")
	target := store.revision
	store.put("idx:metric:cpu", "created-later")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "i",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if len(result.Changes) != 0 {
		t.Fatalf("expected an empty plan, got %v", planOf(result))
	}
	if len(store.writes) != 0 {
		t.Fatalf("internal key was mutated: %v", store.writes)
	}
}

// ---------------------------------------------------------------------------
// SECURITY: blast radius
// ---------------------------------------------------------------------------

func TestMVCCKVService_RestoreToRevision_RejectsOversizedKeyList(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")

	keys := make([]string, MaxRestoreKeys+1)
	for i := range keys {
		keys[i] = "app/k"
	}

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: store.revision,
		Keys:     keys,
	})
	var tooLarge *RestoreScopeTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *RestoreScopeTooLargeError, got %#v", err)
	}
	if tooLarge.Limit != MaxRestoreKeys {
		t.Fatalf("Limit = %d, want %d", tooLarge.Limit, MaxRestoreKeys)
	}
	if len(store.writes) != 0 {
		t.Fatalf("an oversized restore wrote: %v", store.writes)
	}
}

// TestMVCCKVService_RestoreToRevision_RejectsOversizedPrefixScope proves the
// cap refuses the request whole rather than truncating the plan: a partially
// applied logical undo leaves the keyspace in a state that existed at no
// revision.
func TestMVCCKVService_RestoreToRevision_RejectsOversizedPrefixScope(t *testing.T) {
	store := newRestoreStoreMock()
	// Stand the cap down to a testable size by driving the range through a
	// host that reports more matches than the cap allows.
	svc := &MVCCKVService{
		host: &mockHost{
			syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
				if _, ok := query.(MVCCRevisionQuery); ok {
					return int64(100), nil
				}
				result := &MVCCRangeResult{Revision: 100}
				for i := 0; i <= MaxRestoreKeys; i++ {
					result.KeyValues = append(result.KeyValues, &KeyValue{
						Key: []byte("app/" + strings.Repeat("k", i%3) + string(rune('a'+i%26)) + itoa(i)),
					})
				}
				result.Count = int64(len(result.KeyValues))
				return result, nil
			},
			syncProposeFn: store.propose,
		},
		shardID: 1,
	}

	_, err := svc.RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: 50,
		Prefix:   "app/",
	})
	var tooLarge *RestoreScopeTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *RestoreScopeTooLargeError, got %#v", err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("an oversized restore wrote: %v", store.writes)
	}
}

// itoa avoids pulling strconv into the test for one call site.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestMVCCKVService_RestoreToRevision_RejectsUnboundedPrefix(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: store.revision,
		Prefix:   "\xff\xff",
	})
	var unbounded *RestoreUnboundedPrefixError
	if !errors.As(err, &unbounded) {
		t.Fatalf("expected *RestoreUnboundedPrefixError, got %#v", err)
	}
}

// ---------------------------------------------------------------------------
// Apply failures
// ---------------------------------------------------------------------------

func TestMVCCKVService_RestoreToRevision_ReportsApplyFailure(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	store.put("app/b", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.put("app/b", "v2")
	store.proposeErr["app/b"] = errors.New("raft unavailable")

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	var applyErr *RestoreApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("expected *RestoreApplyError, got %#v", err)
	}
	if applyErr.Key != "app/b" {
		t.Fatalf("Key = %q, want app/b", applyErr.Key)
	}
	if applyErr.Action != RestoreActionRevert {
		t.Fatalf("Action = %q, want revert", applyErr.Action)
	}
	if applyErr.Applied != 1 {
		t.Fatalf("Applied = %d, want 1 (app/a committed first)", applyErr.Applied)
	}
	// The earlier change is committed: the restore is not atomic across keys
	// and the error reports exactly how far it got.
	assertStrings(t, "writes", store.writes, []string{"put:app/a=v1"})
}

func TestMVCCKVService_RestoreToRevision_PropagatesCurrentRevisionFailure(t *testing.T) {
	svc := &MVCCKVService{
		host: &mockHost{
			syncReadFn: func(_ context.Context, _ uint64, _ interface{}) (interface{}, error) {
				return nil, errors.New("read failed")
			},
		},
		shardID: 1,
	}
	_, err := svc.RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: 5, Prefix: "app/",
	})
	if err == nil {
		t.Fatal("expected an error when the current revision cannot be read")
	}
}

// ---------------------------------------------------------------------------
// ValidateRestoreRequest (used by every wrapper before namespacing)
// ---------------------------------------------------------------------------

func TestValidateRestoreRequest_AcceptsWellFormedScopes(t *testing.T) {
	if err := ValidateRestoreRequest(&RestoreRevisionRequest{Revision: 1, Prefix: "app/"}); err != nil {
		t.Fatalf("prefix scope rejected: %v", err)
	}
	if err := ValidateRestoreRequest(&RestoreRevisionRequest{Revision: 1, Keys: []string{"app/a"}}); err != nil {
		t.Fatalf("keys scope rejected: %v", err)
	}
}

func TestValidateRestoreRequest_RejectsMalformedRequests(t *testing.T) {
	cases := []struct {
		name string
		req  *RestoreRevisionRequest
		want interface{}
	}{
		{"nil", nil, &RestoreNilRequestError{}},
		{"zero revision", &RestoreRevisionRequest{Prefix: "a"}, &RestoreRevisionInvalidError{}},
		{"no scope", &RestoreRevisionRequest{Revision: 1}, &RestoreScopeEmptyError{}},
		{"both scopes", &RestoreRevisionRequest{Revision: 1, Prefix: "a", Keys: []string{"b"}}, &RestoreScopeAmbiguousError{}},
		{"internal prefix", &RestoreRevisionRequest{Revision: 1, Prefix: "__sys:"}, &RestoreInternalKeyError{}},
		{"internal key", &RestoreRevisionRequest{Revision: 1, Keys: []string{"ts:x"}}, &RestoreInternalKeyError{}},
		{"empty key", &RestoreRevisionRequest{Revision: 1, Keys: []string{""}}, &RestoreEmptyKeyError{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRestoreRequest(tc.req)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got, want := restoreErrName(err), restoreErrName(tc.want.(error)); got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

// restoreErrName names an error's concrete restore-error type so table cases
// can state the expected type without reflection.
func restoreErrName(v error) string {
	switch v.(type) {
	case *RestoreNilRequestError:
		return "RestoreNilRequestError"
	case *RestoreRevisionInvalidError:
		return "RestoreRevisionInvalidError"
	case *RestoreRevisionFutureError:
		return "RestoreRevisionFutureError"
	case *RestoreScopeEmptyError:
		return "RestoreScopeEmptyError"
	case *RestoreScopeAmbiguousError:
		return "RestoreScopeAmbiguousError"
	case *RestoreEmptyKeyError:
		return "RestoreEmptyKeyError"
	case *RestoreInternalKeyError:
		return "RestoreInternalKeyError"
	case *RestoreScopeTooLargeError:
		return "RestoreScopeTooLargeError"
	case *RestoreUnboundedPrefixError:
		return "RestoreUnboundedPrefixError"
	case *RestoreApplyError:
		return "RestoreApplyError"
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// Plan helpers
// ---------------------------------------------------------------------------

func TestDiffRestoreKey_ClassifiesEveryStateCombination(t *testing.T) {
	was := &KeyValue{Key: []byte("k"), Value: []byte("old"), ModRevision: 2}
	is := &KeyValue{Key: []byte("k"), Value: []byte("new"), ModRevision: 7}

	step, skip := diffRestoreKey("k", was, is)
	if skip || step.change.Action != RestoreActionRevert {
		t.Fatalf("both present, differing: got %v skip=%v", step.change.Action, skip)
	}

	step, skip = diffRestoreKey("k", was, nil)
	if skip || step.change.Action != RestoreActionRecreate {
		t.Fatalf("as-of only: got %v skip=%v", step.change.Action, skip)
	}

	step, skip = diffRestoreKey("k", nil, is)
	if skip || step.change.Action != RestoreActionDelete {
		t.Fatalf("current only: got %v skip=%v", step.change.Action, skip)
	}

	same := &KeyValue{Key: []byte("k"), Value: []byte("old"), ModRevision: 9}
	if _, skip = diffRestoreKey("k", was, same); !skip {
		t.Fatal("identical values should be skipped")
	}
	if _, skip = diffRestoreKey("k", nil, nil); !skip {
		t.Fatal("absent in both should be skipped")
	}
}

func TestFilterRestorableKeyValues_DropsInternalAndNilEntries(t *testing.T) {
	out := filterRestorableKeyValues([]*KeyValue{
		{Key: []byte("app/a")},
		nil,
		{Key: []byte("__sys:roles/admin")},
		{Key: []byte("ts:cpu/1")},
		{Key: []byte("idx:metric:cpu")},
		{Key: []byte("idx:tag:host")},
		{Key: []byte("retention:cpu")},
		{Key: []byte("app/b")},
	})
	if len(out) != 2 {
		t.Fatalf("got %d entries, want 2: %v", len(out), out)
	}
	if _, ok := out["app/a"]; !ok {
		t.Fatal("app/a was dropped")
	}
	if _, ok := out["app/b"]; !ok {
		t.Fatal("app/b was dropped")
	}
}

// TestFilterRestorableKeyValues_ExcludesTheRelationalKeyspace pins the
// deliberate exclusion of relational data from a KV-level restore.
//
// It is not an oversight that a table row cannot be restored this way.
// restoreWriter applies its plan through the ordinary Put/Delete path, which
// maintains the KV secondary index and nothing else; a row's "__idx:" entries
// are written only inside the row+index compare-and-swap in
// pkg/sql/provider/relational_writer.go. Restoring a row here would therefore
// produce a row with no index entries -- invisible to indexed lookups, with
// its unique constraints unenforced -- which is a worse state than leaving it
// alone. Relational point-in-time recovery belongs to the snapshot path.
func TestFilterRestorableKeyValues_ExcludesTheRelationalKeyspace(t *testing.T) {
	out := filterRestorableKeyValues([]*KeyValue{
		{Key: []byte("app/a")},
		{Key: []byte("__tbl:shop:1/\x00\x00\x00\x00\x00\x00\x00\x01")},
		{Key: []byte("__idx:shop:1:uq_email/a@b")},
		{Key: []byte("__seq:shop:__autoinc:1")},
		{Key: []byte("__sys:tables/shop/orders")},
	})
	if len(out) != 1 {
		t.Fatalf("got %d entries, want only the user key: %v", len(out), out)
	}
	if _, ok := out["app/a"]; !ok {
		t.Fatalf("the user key was dropped: %v", out)
	}
}

// TestValidateRestorePrefix_RefusesTheRelationalKeyspace covers the other
// half of the same guard: a restore scoped directly at a relational prefix is
// refused outright rather than silently planning nothing.
func TestValidateRestorePrefix_RefusesTheRelationalKeyspace(t *testing.T) {
	for _, prefix := range []string{
		"__tbl:",
		"__tbl:shop:1/",
		"__idx:shop:1:uq_email/",
		"__seq:shop:__autoinc:1",
	} {
		err := ValidateRestoreRequest(&RestoreRevisionRequest{Revision: 7, Prefix: prefix})
		var ike *RestoreInternalKeyError
		if !errors.As(err, &ike) {
			t.Errorf("ValidateRestoreRequest(prefix=%q) = %v, want *RestoreInternalKeyError", prefix, err)
		}
	}
}

func TestRestoreAppliers_CoverEveryPlannedAction(t *testing.T) {
	for _, action := range []RestoreAction{RestoreActionRevert, RestoreActionRecreate, RestoreActionDelete} {
		if _, ok := restoreAppliers[action]; !ok {
			t.Fatalf("no applier registered for action %q", action)
		}
	}
	if len(restoreAppliers) != 3 {
		t.Fatalf("restoreAppliers has %d entries, want 3", len(restoreAppliers))
	}
}

// ---------------------------------------------------------------------------
// Read-path failure branches
// ---------------------------------------------------------------------------

// rangeScriptHost answers MVCCRevisionQuery with a fixed revision and each
// successive MVCCRangeQuery from a script, so the as-of scan and the
// current-state scan can be given different outcomes.
type rangeScriptHost struct {
	revision int64
	results  []*MVCCRangeResult
	errs     []error
	calls    int

	// changed is the revision-index answer; scanErr overrides it.
	changed   []string
	truncated bool
	scanErr   error
}

func (h *rangeScriptHost) read(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
	if _, ok := query.(MVCCRevisionQuery); ok {
		return h.revision, nil
	}
	if _, ok := query.(MVCCScanChangesQuery); ok {
		if h.scanErr != nil {
			return nil, h.scanErr
		}
		return &MVCCScanChangesResult{
			Keys:      h.changed,
			Revision:  h.revision,
			Truncated: h.truncated,
		}, nil
	}
	i := h.calls
	h.calls++
	if i < len(h.errs) && h.errs[i] != nil {
		return nil, h.errs[i]
	}
	if i < len(h.results) {
		return h.results[i], nil
	}
	return &MVCCRangeResult{Revision: h.revision}, nil
}

// rangeOf builds a range result holding n keys under prefix.
func rangeOf(prefix string, n int) *MVCCRangeResult {
	result := &MVCCRangeResult{Revision: 100}
	for i := 0; i < n; i++ {
		result.KeyValues = append(result.KeyValues, &KeyValue{Key: []byte(prefix + itoa(i))})
	}
	result.Count = int64(len(result.KeyValues))
	return result
}

func TestMVCCKVService_RestoreToRevision_PropagatesCurrentRangeFailure(t *testing.T) {
	host := &rangeScriptHost{
		revision: 100,
		results:  []*MVCCRangeResult{rangeOf("app/", 1)},
		errs:     []error{nil, errors.New("follower read failed")},
	}
	svc := &MVCCKVService{host: &mockHost{syncReadFn: host.read}, shardID: 1}

	_, err := svc.RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: 50, Prefix: "app/",
	})
	if err == nil {
		t.Fatal("expected the current-state range failure to propagate")
	}
}

func TestMVCCKVService_RestoreToRevision_RejectsOversizedCurrentRange(t *testing.T) {
	host := &rangeScriptHost{
		revision: 100,
		results: []*MVCCRangeResult{
			rangeOf("app/", 1),
			rangeOf("app/", MaxRestoreKeys+1),
		},
	}
	svc := &MVCCKVService{host: &mockHost{syncReadFn: host.read}, shardID: 1}

	_, err := svc.RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: 50, Prefix: "app/",
	})
	var tooLarge *RestoreScopeTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *RestoreScopeTooLargeError, got %#v", err)
	}
}

// TestMVCCKVService_RestoreToRevision_RejectsOversizedUnion covers the case no
// individual source catches: the live set and the index-derived changed set are
// each within the cap, but they are disjoint, so the candidate set the diff
// would build is larger than either. Deleted keys arrive only through the
// index, so this is the realistic shape of a bulk-delete recovery that is too
// big to attempt in one request.
func TestMVCCKVService_RestoreToRevision_RejectsOversizedUnion(t *testing.T) {
	half := MaxRestoreKeys/2 + 100

	deleted := make([]string, 0, half)
	for i := 0; i < half; i++ {
		deleted = append(deleted, "app/gone-"+itoa(i))
	}

	host := &rangeScriptHost{
		revision: 100,
		results: []*MVCCRangeResult{
			rangeOf("app/live-", half),
			rangeOf("app/live-", half),
		},
		changed: deleted,
	}
	svc := &MVCCKVService{host: &mockHost{syncReadFn: host.read}, shardID: 1}

	_, err := svc.RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: 50, Prefix: "app/",
	})
	var tooLarge *RestoreScopeTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *RestoreScopeTooLargeError, got %#v", err)
	}
}

// TestMVCCKVService_RestoreToRevision_RejectsTruncatedChangeScan proves a
// change scan that hit its limit is a refusal, not a short answer: acting on a
// truncated view would restore some of the deleted keys and silently leave the
// rest deleted.
func TestMVCCKVService_RestoreToRevision_RejectsTruncatedChangeScan(t *testing.T) {
	host := &rangeScriptHost{
		revision:  100,
		results:   []*MVCCRangeResult{rangeOf("app/", 1), rangeOf("app/", 1)},
		changed:   []string{"app/gone"},
		truncated: true,
	}
	svc := &MVCCKVService{host: &mockHost{syncReadFn: host.read}, shardID: 1}

	_, err := svc.RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: 50, Prefix: "app/",
	})
	var tooLarge *RestoreScopeTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected *RestoreScopeTooLargeError, got %#v", err)
	}
}

// TestMVCCKVService_RestoreToRevision_PropagatesUnexpectedReadFailure checks a
// read error that is neither absence nor compaction aborts the restore rather
// than being folded into "the key does not exist" — which would plan a delete
// for a key that is merely unreadable.
func TestMVCCKVService_RestoreToRevision_PropagatesUnexpectedReadFailure(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision

	svc := &MVCCKVService{
		host: &mockHost{
			syncReadFn: func(ctx context.Context, shard uint64, query interface{}) (interface{}, error) {
				if _, ok := query.(MVCCGetQuery); ok {
					return nil, errors.New("state machine unavailable")
				}
				return store.read(ctx, shard, query)
			},
			syncProposeFn: store.propose,
		},
		shardID: 1,
	}

	_, err := svc.RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target, Keys: []string{"app/a"},
	})
	if err == nil {
		t.Fatal("expected the read failure to abort the restore")
	}
	var notFound *KeyNotFoundError
	if errors.As(err, &notFound) {
		t.Fatal("an unreadable key must not be reported as absent")
	}
	if len(store.writes) != 0 {
		t.Fatalf("a restore with an unreadable key wrote: %v", store.writes)
	}
}

func TestMVCCKVService_RestoreToRevision_PropagatesAsOfReadFailureForKeysScope(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision

	svc := &MVCCKVService{
		host: &mockHost{
			syncReadFn: func(ctx context.Context, shard uint64, query interface{}) (interface{}, error) {
				if _, ok := query.(MVCCGetAtRevisionQuery); ok {
					return nil, errors.New("state machine unavailable")
				}
				return store.read(ctx, shard, query)
			},
			syncProposeFn: store.propose,
		},
		shardID: 1,
	}

	_, err := svc.RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target, Keys: []string{"app/a"},
	})
	if err == nil {
		t.Fatal("expected the as-of read failure to abort the restore")
	}
	if len(store.writes) != 0 {
		t.Fatalf("a restore with an unreadable history wrote: %v", store.writes)
	}
}

// ---------------------------------------------------------------------------
// Prefix-scope completeness via the revision index
//
// A range scan walks the LIVE metadata namespace at every revision, so it
// cannot see a key that has been deleted. These tests pin that the durable
// revision index is what closes that gap, and that every guard still applies
// to the keys it contributes.
// ---------------------------------------------------------------------------

// TestMVCCKVService_RestoreToRevision_PrefixRecreatesKeyDeletedAfterTarget is
// the bulk-delete recovery case: every key under a prefix is deleted, and a
// prefix-scoped restore brings them all back.
func TestMVCCKVService_RestoreToRevision_PrefixRecreatesKeyDeletedAfterTarget(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	store.put("app/b", "v2")
	store.put("app/c", "v3")
	target := store.revision

	// A bulk delete wipes the prefix.
	store.del("app/a")
	store.del("app/b")
	store.del("app/c")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}

	assertStrings(t, "plan", planOf(result), []string{
		"recreate app/a", "recreate app/b", "recreate app/c",
	})
	assertStrings(t, "writes", store.writes, []string{
		"put:app/a=v1", "put:app/b=v2", "put:app/c=v3",
	})
	if result.Applied != 3 {
		t.Fatalf("Applied = %d, want 3", result.Applied)
	}

	for key, want := range map[string]string{"app/a": "v1", "app/b": "v2", "app/c": "v3"} {
		kv, kerr := store.at(key, 0)
		if kerr != nil || string(kv.Value) != want {
			t.Fatalf("%s after restore: %v, %v (want %q)", key, kv, kerr, want)
		}
	}
}

// TestMVCCKVService_RestoreToRevision_PrefixHandlesMixedDeleteAndUpdate checks
// the index-derived and range-derived candidates combine into one plan.
func TestMVCCKVService_RestoreToRevision_PrefixHandlesMixedDeleteAndUpdate(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/changed", "v1")
	store.put("app/deleted", "v1")
	store.put("app/same", "v1")
	target := store.revision

	store.put("app/changed", "v2")
	store.del("app/deleted")
	store.put("app/added", "new")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}

	assertStrings(t, "plan", planOf(result), []string{
		"delete app/added", "revert app/changed", "recreate app/deleted",
	})
	if result.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1 (app/same)", result.Skipped)
	}
}

// TestMVCCKVService_RestoreToRevision_IndexDerivedKeysRespectInternalGuard is
// the security case for the new code path. The revision index reaches keys a
// range scan cannot, so a deleted "__sys:roles/…" record is exactly what an
// attacker would want a prefix restore to resurrect through it.
func TestMVCCKVService_RestoreToRevision_IndexDerivedKeysRespectInternalGuard(t *testing.T) {
	cases := []struct {
		name        string
		prefix      string
		userKey     string
		internalKey string
	}{
		{"underscore spans __sys:", "_", "_user/a", "__sys:roles/admin"},
		{"i spans idx:metric:", "i", "items/a", "idx:metric:cpu"},
		{"i spans idx:tag:", "i", "items/a", "idx:tag:host=web1"},
		{"r spans retention:", "r", "reports/a", "retention:cpu"},
		{"t spans ts:", "t", "tasks/a", "ts:cpu/1700000000"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newRestoreStoreMock()
			store.put(tc.userKey, "v1")
			store.put(tc.internalKey, "internal-v1")
			target := store.revision

			// Both are DELETED, so both are reachable only through the index.
			store.del(tc.userKey)
			store.del(tc.internalKey)

			result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
				Revision: target,
				Prefix:   tc.prefix,
			})
			if err != nil {
				t.Fatalf("RestoreToRevision: %v", err)
			}

			// The user key comes back; the internal one stays deleted.
			assertStrings(t, "plan", planOf(result), []string{"recreate " + tc.userKey})
			assertStrings(t, "writes", store.writes, []string{"put:" + tc.userKey + "=v1"})

			if _, kerr := store.at(tc.internalKey, 0); kerr == nil {
				t.Fatalf("internal key %q was resurrected through the revision index", tc.internalKey)
			}
		})
	}
}

// TestMVCCKVService_RestoreToRevision_SurfacesChangeIndexRefusal proves a
// backend that cannot answer the change scan produces a refusal, not an empty
// plan that looks like a successful no-op restore.
func TestMVCCKVService_RestoreToRevision_SurfacesChangeIndexRefusal(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.del("app/a")
	store.scanErr = &noChangeIndexError{}

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	var unavailable *RestoreChangeIndexUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected *RestoreChangeIndexUnavailableError, got %#v", err)
	}
	if len(store.writes) != 0 {
		t.Fatalf("a refused restore wrote: %v", store.writes)
	}
}

// TestMVCCKVService_RestoreToRevision_KeysScopeNeedsNoChangeIndex checks the
// explicit Keys scope still works on a backend with no revision index: it
// reads each key's history directly and never issues the scan.
func TestMVCCKVService_RestoreToRevision_KeysScopeNeedsNoChangeIndex(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.del("app/a")
	store.scanErr = &noChangeIndexError{}

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Keys:     []string{"app/a"},
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	assertStrings(t, "plan", planOf(result), []string{"recreate app/a"})
}

// TestMVCCKVService_RestoreToRevision_PropagatesChangeScanFailure checks an
// ordinary scan failure aborts the restore rather than yielding a partial
// candidate set.
func TestMVCCKVService_RestoreToRevision_PropagatesChangeScanFailure(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.scanErr = errors.New("index read failed")

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err == nil {
		t.Fatal("expected the change-scan failure to abort the restore")
	}
	if len(store.writes) != 0 {
		t.Fatalf("a failed restore wrote: %v", store.writes)
	}
}

func TestMVCCKVService_RestoreToRevision_RejectsUnexpectedChangeScanResultType(t *testing.T) {
	svc := &MVCCKVService{
		host: &mockHost{
			syncReadFn: func(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
				switch query.(type) {
				case MVCCRevisionQuery:
					return int64(100), nil
				case MVCCScanChangesQuery:
					return "not a scan result", nil
				}
				return &MVCCRangeResult{Revision: 100}, nil
			},
		},
		shardID: 1,
	}
	_, err := svc.RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: 50, Prefix: "app/",
	})
	var readErr *ReadError
	if !errors.As(err, &readErr) {
		t.Fatalf("expected *ReadError, got %#v", err)
	}
}

// noChangeIndexError satisfies the changeIndexUnsupportedErr interface the way
// an injected MVCCStore without a revision index refuses ScanChanges.
type noChangeIndexError struct{}

func (e *noChangeIndexError) Error() string           { return "no revision index" }
func (e *noChangeIndexError) ScanChangesUnsupported() {}

// ---------------------------------------------------------------------------
// Compare-and-swap guard
// ---------------------------------------------------------------------------

// TestMVCCKVService_RestoreToRevision_CASSkipsConcurrentlyModifiedKey is the
// core no-clobber guarantee: a live writer updates a key in scope between the
// diff and the write, and the restore leaves that writer's value alone.
func TestMVCCKVService_RestoreToRevision_CASSkipsConcurrentlyModifiedKey(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	store.put("app/b", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.put("app/b", "v2")

	// A concurrent writer touches app/a after the plan is computed but before
	// the first change is applied.
	store.beforeApply = func() { store.put("app/a", "written-by-someone-else") }

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}

	// The plan still reports both keys; only app/b was applied.
	assertStrings(t, "plan", planOf(result), []string{"revert app/a", "revert app/b"})
	if result.Applied != 1 || result.Conflicted != 1 {
		t.Fatalf("applied=%d conflicted=%d, want 1/1", result.Applied, result.Conflicted)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Key != "app/a" {
		t.Fatalf("conflicts = %+v, want app/a", result.Conflicts)
	}

	// The concurrent writer's value survived.
	kv, err := store.at("app/a", 0)
	if err != nil || string(kv.Value) != "written-by-someone-else" {
		t.Fatalf("the concurrent write was clobbered: %v, %v", kv, err)
	}
	// The uncontended key was restored.
	kv, err = store.at("app/b", 0)
	if err != nil || string(kv.Value) != "v1" {
		t.Fatalf("app/b was not restored: %v, %v", kv, err)
	}
}

// TestMVCCKVService_RestoreToRevision_ForceOverwritesConcurrentModification is
// the escape hatch: the same race, with Force set, applies anyway.
func TestMVCCKVService_RestoreToRevision_ForceOverwritesConcurrentModification(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.beforeApply = func() { store.put("app/a", "written-by-someone-else") }

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
		Force:    true,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result.Applied != 1 || result.Conflicted != 0 {
		t.Fatalf("applied=%d conflicted=%d, want 1/0", result.Applied, result.Conflicted)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("Force must report no conflicts, got %+v", result.Conflicts)
	}

	kv, err := store.at("app/a", 0)
	if err != nil || string(kv.Value) != "v1" {
		t.Fatalf("Force did not overwrite: %v, %v", kv, err)
	}
}

// TestMVCCKVService_RestoreToRevision_CASGuardsRecreateAgainstResurrection
// checks the recreate case: if someone re-created the key after the plan was
// computed, the restore must not overwrite their value with the historical one.
func TestMVCCKVService_RestoreToRevision_CASGuardsRecreateAgainstResurrection(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.del("app/a")
	store.beforeApply = func() { store.put("app/a", "recreated-by-someone-else") }

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Keys:     []string{"app/a"},
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	assertStrings(t, "plan", planOf(result), []string{"recreate app/a"})
	if result.Applied != 0 || result.Conflicted != 1 {
		t.Fatalf("applied=%d conflicted=%d, want 0/1", result.Applied, result.Conflicted)
	}

	kv, err := store.at("app/a", 0)
	if err != nil || string(kv.Value) != "recreated-by-someone-else" {
		t.Fatalf("the concurrent recreate was clobbered: %v, %v", kv, err)
	}
}

// TestMVCCKVService_RestoreToRevision_CASGuardsDeleteAgainstConcurrentWrite
// checks the delete case: a key the plan would remove must not be removed if
// someone wrote a newer value to it in the meantime.
func TestMVCCKVService_RestoreToRevision_CASGuardsDeleteAgainstConcurrentWrite(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/keep", "v1")
	target := store.revision
	store.put("app/new", "created-after-target")
	store.beforeApply = func() { store.put("app/new", "updated-again") }

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	assertStrings(t, "plan", planOf(result), []string{"delete app/new"})
	if result.Applied != 0 || result.Conflicted != 1 {
		t.Fatalf("applied=%d conflicted=%d, want 0/1", result.Applied, result.Conflicted)
	}

	kv, err := store.at("app/new", 0)
	if err != nil || string(kv.Value) != "updated-again" {
		t.Fatalf("the concurrent write was deleted: %v, %v", kv, err)
	}
}

// TestMVCCKVService_RestoreToRevision_CASConflictDoesNotHaltTheRestore checks a
// conflict is a per-key outcome, not a request-level failure: the remaining
// changes still apply.
func TestMVCCKVService_RestoreToRevision_CASConflictDoesNotHaltTheRestore(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	store.put("app/b", "v1")
	store.put("app/c", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.put("app/b", "v2")
	store.put("app/c", "v2")
	store.beforeApply = func() { store.put("app/a", "moved-on") }

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result.Applied != 2 || result.Conflicted != 1 {
		t.Fatalf("applied=%d conflicted=%d, want 2/1", result.Applied, result.Conflicted)
	}
	for _, key := range []string{"app/b", "app/c"} {
		kv, kerr := store.at(key, 0)
		if kerr != nil || string(kv.Value) != "v1" {
			t.Fatalf("%s was not restored after the conflict: %v, %v", key, kv, kerr)
		}
	}
}

// TestMVCCKVService_RestoreToRevision_CASDryRunWritesNothing checks the guard
// did not change dry-run behaviour: still no writes, still no conflicts.
func TestMVCCKVService_RestoreToRevision_CASDryRunWritesNothing(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	store.put("app/gone", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.del("app/gone")
	revBefore := store.revision

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	assertStrings(t, "plan", planOf(result), []string{"revert app/a", "recreate app/gone"})
	if result.Applied != 0 || result.Conflicted != 0 || len(result.Conflicts) != 0 {
		t.Fatalf("dry run reported applied=%d conflicted=%d", result.Applied, result.Conflicted)
	}
	if len(store.writes) != 0 || store.revision != revBefore {
		t.Fatalf("dry run mutated the store: writes=%v rev %d->%d", store.writes, revBefore, store.revision)
	}
}

// TestMVCCKVService_RestoreToRevision_ForceDryRunAlsoWritesNothing pins that
// Force does not turn a dry run into an apply.
func TestMVCCKVService_RestoreToRevision_ForceDryRunAlsoWritesNothing(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target, Prefix: "app/", DryRun: true, Force: true,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result.Applied != 0 || len(store.writes) != 0 {
		t.Fatalf("forced dry run wrote: applied=%d writes=%v", result.Applied, store.writes)
	}
}

// TestMVCCKVService_RestoreToRevision_CASReportsProposeFailure checks a genuine
// transaction failure is still an error, distinct from a conflict.
func TestMVCCKVService_RestoreToRevision_CASReportsProposeFailure(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.proposeErr["app/a"] = errors.New("raft unavailable")

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target, Prefix: "app/",
	})
	var applyErr *RestoreApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("expected *RestoreApplyError, got %#v", err)
	}
	if applyErr.Key != "app/a" {
		t.Fatalf("Key = %q, want app/a", applyErr.Key)
	}
}

func TestRestoreGuard_ComparesModRevisionForEveryAction(t *testing.T) {
	cases := []struct {
		action RestoreAction
		from   int64
	}{
		{RestoreActionRevert, 7},
		{RestoreActionDelete, 9},
		{RestoreActionRecreate, 0}, // 0 == "still absent"
	}
	for _, tc := range cases {
		guard := restoreGuard(restoreStep{change: RestoreKeyChange{
			Key: "k", Action: tc.action, FromRevision: tc.from,
		}})
		if guard.Target != CompareModRevision || guard.Result != CompareEqual {
			t.Fatalf("%s: target/result = %v/%v", tc.action, guard.Target, guard.Result)
		}
		if guard.ModRevision != tc.from {
			t.Fatalf("%s: ModRevision = %d, want %d", tc.action, guard.ModRevision, tc.from)
		}
		if string(guard.Key) != "k" {
			t.Fatalf("%s: Key = %q", tc.action, guard.Key)
		}
	}
}

func TestRestoreCASOps_CoverEveryPlannedAction(t *testing.T) {
	for _, action := range []RestoreAction{RestoreActionRevert, RestoreActionRecreate, RestoreActionDelete} {
		if _, ok := restoreCASOps[action]; !ok {
			t.Fatalf("no CAS operation registered for action %q", action)
		}
	}
	if len(restoreCASOps) != len(restoreAppliers) {
		t.Fatalf("restoreCASOps has %d entries and restoreAppliers %d: the two write paths must cover the same actions",
			len(restoreCASOps), len(restoreAppliers))
	}

	put := restoreCASOps[RestoreActionRevert](restoreStep{
		change: RestoreKeyChange{Key: "k"}, value: []byte("v"),
	})
	if put.Type != OpPut || string(put.Key) != "k" || string(put.Value) != "v" {
		t.Fatalf("revert op = %+v", put)
	}
	del := restoreCASOps[RestoreActionDelete](restoreStep{change: RestoreKeyChange{Key: "k"}})
	if del.Type != OpDelete || string(del.Key) != "k" || del.Value != nil {
		t.Fatalf("delete op = %+v", del)
	}
}

func TestRestoreCandidates_UnionsAndSortsAndFiltersInternal(t *testing.T) {
	live := map[string]*KeyValue{
		"app/b": {Key: []byte("app/b")},
		"app/a": {Key: []byte("app/a")},
	}
	changed := []string{"app/c", "app/a", "__sys:roles/admin", "ts:cpu/1"}

	got := restoreCandidates(live, changed)
	assertStrings(t, "candidates", got, []string{"app/a", "app/b", "app/c"})
}

// TestMVCCKVService_RestoreToRevision_ForceDeletesKeyCreatedAfterTarget covers
// the forced delete path: with Force the delete is an unconditional proposal
// rather than a guarded transaction.
func TestMVCCKVService_RestoreToRevision_ForceDeletesKeyCreatedAfterTarget(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/keep", "v1")
	target := store.revision
	store.put("app/new", "created-after-target")
	store.beforeApply = func() { store.put("app/new", "updated-again") }

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
		Force:    true,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	assertStrings(t, "plan", planOf(result), []string{"delete app/new"})
	assertStrings(t, "writes", store.writes, []string{"del:app/new"})
	if result.Applied != 1 || result.Conflicted != 0 {
		t.Fatalf("applied=%d conflicted=%d, want 1/0", result.Applied, result.Conflicted)
	}
	if _, kerr := store.at("app/new", 0); kerr == nil {
		t.Fatal("Force did not delete the key created after the target revision")
	}
}

// TestMVCCKVService_RestoreToRevision_ForceRecreatesDeletedKey covers the
// forced recreate path end to end.
func TestMVCCKVService_RestoreToRevision_ForceRecreatesDeletedKey(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.del("app/a")

	result, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target,
		Prefix:   "app/",
		Force:    true,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	assertStrings(t, "writes", store.writes, []string{"put:app/a=v1"})
	if result.Applied != 1 {
		t.Fatalf("Applied = %d, want 1", result.Applied)
	}
}

// TestMVCCKVService_RestoreToRevision_ForceReportsProposeFailure covers the
// forced write path's error branch.
func TestMVCCKVService_RestoreToRevision_ForceReportsProposeFailure(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/a", "v1")
	target := store.revision
	store.put("app/a", "v2")
	store.proposeErr["app/a"] = errors.New("raft unavailable")

	_, err := store.service().RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target, Prefix: "app/", Force: true,
	})
	var applyErr *RestoreApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("expected *RestoreApplyError, got %#v", err)
	}
}

// TestMVCCKVService_RestoreToRevision_PrefixPropagatesHistoricalReadFailure
// covers the fail-closed path for an index-derived candidate: the change scan
// says a key was deleted, but its historical value cannot be read. Planning a
// change from an unreadable history is exactly what must not happen.
func TestMVCCKVService_RestoreToRevision_PrefixPropagatesHistoricalReadFailure(t *testing.T) {
	store := newRestoreStoreMock()
	store.put("app/gone", "v1")
	target := store.revision
	store.del("app/gone")

	svc := &MVCCKVService{
		host: &mockHost{
			syncReadFn: func(ctx context.Context, shard uint64, query interface{}) (interface{}, error) {
				// Only the per-key historical read fails; the ranges and the
				// change scan succeed, so the key reaches the candidate set.
				if _, ok := query.(MVCCGetAtRevisionQuery); ok {
					return nil, errors.New("state machine unavailable")
				}
				return store.read(ctx, shard, query)
			},
			syncProposeFn: store.propose,
		},
		shardID: 1,
	}

	_, err := svc.RestoreToRevision(context.Background(), &RestoreRevisionRequest{
		Revision: target, Prefix: "app/",
	})
	if err == nil {
		t.Fatal("expected the historical read failure to abort the restore")
	}
	if len(store.writes) != 0 {
		t.Fatalf("a failed restore wrote: %v", store.writes)
	}
}
