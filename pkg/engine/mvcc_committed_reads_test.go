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

package engine

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// newEngineWithOpenBatch returns an engine holding revision 1 committed and
// revision 2 assigned and staged in an apply batch that has not committed: the
// window in which the apply counter names a revision storage does not hold.
func newEngineWithOpenBatch(t *testing.T) *MVCCEngine {
	t.Helper()
	eng, stg := newMVCCEngine(t)
	t.Cleanup(func() { _ = stg.Close() })
	t.Cleanup(func() { _ = eng.Close(context.Background()) })

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v0"), 0)})
	b := stg.Batch()
	t.Cleanup(func() { _ = b.Close() })
	if _, _, err := eng.UpdateEntries(b, []sm.Entry{mvccPutEntry(2, []byte("k"), []byte("v1"), 0)}, make([]sm.Result, 1)); err != nil {
		t.Fatalf("UpdateEntries: %v", err)
	}
	if got := eng.Store().CurrentRevision(); got != 2 {
		t.Fatalf("apply counter with the batch open = %d, want 2", got)
	}
	return eng
}

// TestMVCCEngine_RevisionQueryAnswersTheCommittedRevision: the revision query
// backs the service's CurrentRevision, the latest committed revision. Answering
// with the apply counter named revision 2 while its batch was open, and a watch
// cursor resumed from that answer + 1 skipped revision 2 for good.
func TestMVCCEngine_RevisionQueryAnswersTheCommittedRevision(t *testing.T) {
	eng := newEngineWithOpenBatch(t)

	got, err := eng.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if rev, ok := got.(int64); !ok || rev != 1 {
		t.Fatalf("revision query with revision 2 uncommitted = %v, want 1", got)
	}
}

// TestMVCCEngine_RevisionQueryFollowsACommittedUpdate: once an update commits,
// the revision query reports it.
func TestMVCCEngine_RevisionQueryFollowsACommittedUpdate(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("a"), []byte("v0"), 0),
		mvccPutEntry(2, []byte("b"), []byte("v0"), 0),
	})
	got, err := eng.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if rev, ok := got.(int64); !ok || rev != 2 {
		t.Fatalf("revision query after two committed puts = %v, want 2", got)
	}
}

// TestMVCCEngine_ScanChangesReportsTheCommittedRevision: the change scan reads
// committed storage, so the revision it reports, which callers resume the next
// scan from, must be the committed one. Reporting the apply counter covered
// revision 2 while its change was not yet scannable, and the next scan, starting
// after it, never saw that change.
func TestMVCCEngine_ScanChangesReportsTheCommittedRevision(t *testing.T) {
	eng := newEngineWithOpenBatch(t)

	got, err := eng.Lookup(context.Background(), service.MVCCScanChangesQuery{Prefix: []byte("k"), StartRevision: 1})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	res, ok := got.(*service.MVCCScanChangesResult)
	if !ok {
		t.Fatalf("scan result = %T, want *service.MVCCScanChangesResult", got)
	}
	if res.Revision != 1 {
		t.Fatalf("scan revision with revision 2 uncommitted = %d, want 1", res.Revision)
	}
}

// TestMVCCEngine_ScanChangesFollowsACommittedUpdate: once an update commits,
// the scan reports its revision and its change.
func TestMVCCEngine_ScanChangesFollowsACommittedUpdate(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("k1"), []byte("v0"), 0),
		mvccPutEntry(2, []byte("k2"), []byte("v0"), 0),
	})
	got, err := eng.Lookup(context.Background(), service.MVCCScanChangesQuery{Prefix: []byte("k"), StartRevision: 1, EndRevision: 2})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	res, ok := got.(*service.MVCCScanChangesResult)
	if !ok {
		t.Fatalf("scan result = %T, want *service.MVCCScanChangesResult", got)
	}
	if res.Revision != 2 || len(res.Keys) != 2 || res.Keys[0] != "k1" || res.Keys[1] != "k2" {
		t.Fatalf("scan after two committed puts = rev %d, keys %q; want rev 2, keys [k1 k2]", res.Revision, res.Keys)
	}
}
