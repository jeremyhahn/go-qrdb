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

package vlog

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"
	"github.com/transparency-dev/merkle/testonly"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

const auditTenant = "acme"

var auditID = AuditLogID(auditTenant)

// linked returns an encoder that links a body named n to the previous
// digest the way pkg/audit does: the body carries the digest, and the next
// digest folds the body onto it.
func linked(n int) Encoder {
	return func(prev [32]byte, _ bool) ([]byte, [32]byte, error) {
		body := fmt.Appendf(nil, "event-%d prev=%x", n, prev)
		return body, sha256.Sum256(append(prev[:], body...)), nil
	}
}

// appendLinear appends events [from, to) through Append, the OpAuditAppend
// path.
func appendLinear(t *testing.T, c *Chain, w Writer, from, to int) {
	t.Helper()
	for n := from; n < to; n++ {
		if _, err := c.Append(w, auditTenant, linked(n)); err != nil {
			t.Fatalf("Append(%d): %v", n, err)
		}
	}
}

// appendInstance appends events [from, to) through AppendAudit, the
// OpVerifiableLog path.
func appendInstance(t *testing.T, c *Chain, w Writer, from, to int) {
	t.Helper()
	for n := from; n < to; n++ {
		if _, err := c.AppendAudit(w, auditTenant, linked(n)); err != nil {
			t.Fatalf("AppendAudit(%d): %v", n, err)
		}
	}
}

// eventBodies reads the linear chain's records [fromSeq, toSeq].
func eventBodies(t *testing.T, r storage.Reader, fromSeq, toSeq uint64) [][]byte {
	t.Helper()
	var out [][]byte
	for seq := fromSeq; seq <= toSeq; seq++ {
		b, err := r.Get(EventKey(auditTenant, seq))
		if err != nil || b == nil {
			t.Fatalf("event %d: %q, %v", seq, b, err)
		}
		out = append(out, b)
	}
	return out
}

// countPrefix counts the keys under prefix.
func countPrefix(t *testing.T, r storage.Reader, prefix string) int {
	t.Helper()
	it := r.NewIterator(&storage.IteratorOptions{LowerBound: []byte(prefix), UpperBound: prefixEnd([]byte(prefix))})
	defer func() { _ = it.Close() }()
	n := 0
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		n++
	}
	return n
}

func TestDescriptor_TreeStartJSON(t *testing.T) {
	plain, err := json.Marshal(testDescriptor(true))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "tree_start") || strings.Contains(string(plain), `"audit"`) {
		t.Fatalf("a named log's descriptor carries tree_start or audit: %s", plain)
	}
	raw, err := json.Marshal(AuditDescriptor(auditTenant, 7))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"origin":"acme/system/audit","hash":"sha256","allow_expire_bodies":true,"tree_start":7,"audit":true}`
	if string(raw) != want {
		t.Fatalf("audit descriptor = %s, want %s", raw, want)
	}
	var back Descriptor
	if err := json.Unmarshal(raw, &back); err != nil || back.TreeStart != 7 || !back.Audit {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
}

func TestAuditLogID(t *testing.T) {
	if auditID != (LogID{TenantID: auditTenant, Database: "system", Log: "audit"}) || !IsAuditLog(auditID) {
		t.Fatalf("AuditLogID = %v", auditID)
	}
	if IsAuditLog(testLog) || IsAuditLog(LogID{TenantID: auditTenant, Database: "system", Log: "other"}) {
		t.Fatal("IsAuditLog accepts a log that is not the audit instance")
	}
}

func TestAuditInstance_ReservedOnTheNamedLogPaths(t *testing.T) {
	stg := memory.NewStorage()
	c := NewChain()
	var re *ReservedLogError
	if err := c.Create(stg, auditID, AuditDescriptor(auditTenant, 0)); !errors.As(err, &re) {
		t.Fatalf("Create(audit) = %v, want *ReservedLogError", err)
	}
	appendInstance(t, c, stg, 0, 1)
	if _, err := c.AppendEntries(stg, auditID, [][]byte{[]byte("x")}); !errors.As(err, &re) {
		t.Fatalf("AppendEntries(audit) = %v, want *ReservedLogError", err)
	}
	desc := testDescriptor(false)
	desc.TreeStart = 3
	var ide *InvalidDescriptorError
	if err := c.Create(stg, testLog, desc); !errors.As(err, &ide) {
		t.Fatalf("Create with tree_start = %v, want *InvalidDescriptorError", err)
	}
	if re.Error() == "" {
		t.Fatal("empty error text")
	}
}

func TestAuditInstance_GateOffWritesNoTreeNode(t *testing.T) {
	stg := memory.NewStorage()
	c := NewChain()
	appendLinear(t, c, stg, 0, 5)
	if n := countPrefix(t, stg, LogTablePrefix); n != 0 {
		t.Fatalf("%d named-log keys after OpAuditAppend alone, want 0", n)
	}
	// One head and five events: exactly the linear chain.
	if n := countPrefix(t, stg, TablePrefix); n != 6 {
		t.Fatalf("%d audit-table keys, want 6", n)
	}
	if _, err := ReadHead(stg, auditID); err == nil {
		t.Fatal("the audit instance exists before any OpVerifiableLog append")
	}
}

func TestAuditInstance_SwitchStartsTheTreeAtS0(t *testing.T) {
	stg := memory.NewStorage()
	c := NewChain()
	appendLinear(t, c, stg, 0, 3)
	preHead := c.heads[auditTenant]

	appendInstance(t, c, stg, 3, 4)
	desc, err := ReadDescriptor(stg, auditID)
	if err != nil || desc.TreeStart != 4 || !desc.AllowExpireBodies || desc.Origin != "acme/system/audit" {
		t.Fatalf("descriptor = %+v, %v, want TreeStart 4", desc, err)
	}
	got, err := Get(stg, auditID, 0, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("Get(0) = %v, %v", got, err)
	}
	// Leaf 0 is the record at S₀, carrying the pre-switch digest.
	if want := fmt.Appendf(nil, "event-3 prev=%x", preHead.digest); !bytes.Equal(got[0].Body, want) {
		t.Fatalf("leaf 0 = %q, want %q", got[0].Body, want)
	}
	// The linear chain continued: seq 4 and the digest folds it.
	if h := c.heads[auditTenant]; h.seq != 4 || h.digest != sha256.Sum256(append(preHead.digest[:], got[0].Body...)) {
		t.Fatalf("linear head = %+v, want seq 4 folding leaf 0", h)
	}

	// An OpAuditAppend applied after the switch is the next leaf as well, so
	// leaf i stays the event at S₀+i.
	appendLinear(t, c, stg, 4, 6)
	appendInstance(t, c, stg, 6, 40)
	head, err := ReadHead(stg, auditID)
	if err != nil || head.Size != 37 {
		t.Fatalf("ReadHead = %+v, %v, want size 37", head, err)
	}
	bodies := eventBodies(t, stg, 4, 40)
	ref := testonly.New(rfc6962.DefaultHasher)
	ref.AppendData(bodies...)
	if !bytes.Equal(head.Root, ref.HashAt(37)) {
		t.Fatal("the instance's root is not the tree of the events from S₀")
	}
	verifyAuditProofs(t, stg, 37, ref)
	page, err := Get(stg, auditID, 30, 10)
	if err != nil || len(page) != 7 || page[0].Index != 30 || !bytes.Equal(page[6].Body, bodies[36]) {
		t.Fatalf("Get(30, 10) = %d entries, %v", len(page), err)
	}

	// Restore reads TreeStart back: the next append is the next leaf.
	again := NewChain()
	if err := again.Restore(stg); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	appendLinear(t, again, stg, 40, 41)
	if head, _ := ReadHead(stg, auditID); head.Size != 38 {
		t.Fatalf("size after restore and append = %d, want 38", head.Size)
	}
}

// TestAuditInstance_LegacyLogUnderTheAuditName pins that a log created under
// the audit name before the instance existed (an alpha Create allowed it) is
// neither reinterpreted nor corrupted: its descriptor has no "audit" field,
// so its entries read back from its own prefix, the instance cannot be
// created over it (AuditNameTakenError, with the linear chain unchanged),
// and OpAuditAppend and Prune extend and prune the linear chain alone.
func TestAuditInstance_LegacyLogUnderTheAuditName(t *testing.T) {
	stg := memory.NewStorage()
	// The pre-instance layout: Create's descriptor and empty head, then two
	// entries under the log's own prefix.
	legacy := `{"origin":"acme/system/audit","hash":"sha256","allow_expire_bodies":true}`
	if err := stg.Set(descriptorKey(auditID), []byte(legacy)); err != nil {
		t.Fatal(err)
	}
	if err := stg.Set(logHeadKey(auditID), encodeLogHead(0, 0, nil)); err != nil {
		t.Fatal(err)
	}
	own := [][]byte{[]byte("own-0"), []byte("own-1")}
	for i, e := range own {
		if err := stg.Set(entryKey(auditID, uint64(i)), e); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := appendLeaves(stg, auditID, logState{allowExpire: true}, own); err != nil {
		t.Fatalf("appendLeaves: %v", err)
	}
	c := NewChain()
	if err := c.Restore(stg); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	appendLinear(t, c, stg, 0, 2)
	headBefore := c.heads[auditTenant]
	linearBefore := tableSnapshot(t, stg, TablePrefix)
	logsBefore := tableSnapshot(t, stg, LogTablePrefix)

	readOwn := func() {
		t.Helper()
		got, err := Get(stg, auditID, 0, 10)
		if err != nil || len(got) != 2 || string(got[0].Body) != "own-0" || string(got[1].Body) != "own-1" {
			t.Fatalf("Get = %+v, %v; want the log's own two entries", got, err)
		}
	}
	readOwn()

	_, err := c.AppendAudit(stg, auditTenant, linked(2))
	var taken *AuditNameTakenError
	if !errors.As(err, &taken) || taken.ID != auditID {
		t.Fatalf("AppendAudit over the legacy log = %v, want AuditNameTakenError", err)
	}
	if c.heads[auditTenant] != headBefore || !reflect.DeepEqual(tableSnapshot(t, stg, TablePrefix), linearBefore) {
		t.Fatal("a refused instance append changed the linear chain")
	}
	if !reflect.DeepEqual(tableSnapshot(t, stg, LogTablePrefix), logsBefore) {
		t.Fatal("a refused instance append changed the legacy log")
	}

	// OpAuditAppend and Prune keep the pre-instance behaviour: the linear
	// chain moves, the legacy log does not.
	appendLinear(t, c, stg, 2, 4)
	if _, err := c.Prune(stg, auditTenant, 3); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if seq, lo, _ := c.Head(auditTenant); seq != 4 || lo != 3 {
		t.Fatalf("linear head = seq %d lo %d, want 4 and 3", seq, lo)
	}
	if !reflect.DeepEqual(tableSnapshot(t, stg, LogTablePrefix), logsBefore) {
		t.Fatal("OpAuditAppend or Prune changed the legacy log")
	}
	readOwn()
}

// tableSnapshot answers every key under prefix with its value.
func tableSnapshot(t *testing.T, r storage.Reader, prefix string) map[string]string {
	t.Helper()
	out := map[string]string{}
	it := r.NewIterator(&storage.IteratorOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	if err := it.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// verifyAuditProofs checks inclusion and consistency proofs of the audit
// instance against ref at every size up to n.
func verifyAuditProofs(t *testing.T, r storage.Reader, n uint64, ref *testonly.Tree) {
	t.Helper()
	h := rfc6962.DefaultHasher
	for size := uint64(1); size <= n; size++ {
		root := ref.HashAt(size)
		for _, index := range sampleBelow(size) {
			got, err := Inclusion(r, auditID, index, size)
			if err != nil {
				t.Fatalf("Inclusion(%d, %d): %v", index, size, err)
			}
			if err := proof.VerifyInclusion(h, index, size, ref.LeafHash(index), got, root); err != nil {
				t.Fatalf("VerifyInclusion(%d, %d): %v", index, size, err)
			}
		}
		for _, size1 := range sampleBelow(size) {
			got, err := Consistency(r, auditID, size1, size)
			if err != nil {
				t.Fatalf("Consistency(%d, %d): %v", size1, size, err)
			}
			if err := proof.VerifyConsistency(h, size1, size, got, ref.HashAt(size1), root); err != nil {
				t.Fatalf("VerifyConsistency(%d, %d): %v", size1, size, err)
			}
		}
	}
}

func TestAuditInstance_PruneIsExpireBodies(t *testing.T) {
	stg := memory.NewStorage()
	c := NewChain()
	appendLinear(t, c, stg, 0, 5)    // seq 1..5
	appendInstance(t, c, stg, 5, 15) // seq 6..15: leaves 0..9, S₀ = 6
	before, err := ReadHead(stg, auditID)
	if err != nil {
		t.Fatal(err)
	}

	// Pruning only below S₀ leaves every leaf's body.
	if n, err := c.Prune(stg, auditTenant, 4); err != nil || n != 3 {
		t.Fatalf("Prune(4) = %d, %v, want 3", n, err)
	}
	if h, _ := ReadHead(stg, auditID); h.BodiesFrom != 0 {
		t.Fatalf("BodiesFrom after a pre-tree prune = %d, want 0", h.BodiesFrom)
	}
	// Prune past S₀ expires leaf bodies and keeps the tree.
	if n, err := c.Prune(stg, auditTenant, 9); err != nil || n != 5 {
		t.Fatalf("Prune(9) = %d, %v, want 5", n, err)
	}
	after, err := ReadHead(stg, auditID)
	if err != nil || after.Size != 10 || after.BodiesFrom != 3 || !bytes.Equal(after.Root, before.Root) {
		t.Fatalf("head after Prune = %+v, %v, want size 10, BodiesFrom 3, same root", after, err)
	}
	got, err := Get(stg, auditID, 2, 2)
	if err != nil || !got[0].Expired || got[1].Expired || got[1].Body == nil {
		t.Fatalf("Get across the cutoff = %v, %v", got, err)
	}

	// ExpireBodies on the instance is in leaf space: below leaf 5 is below
	// sequence S₀+5 = 11.
	if n, err := c.ExpireBodies(stg, auditID, 5); err != nil || n != 2 {
		t.Fatalf("ExpireBodies(5) = %d, %v, want 2", n, err)
	}
	if lo := c.heads[auditTenant].lo; lo != 11 {
		t.Fatalf("linear lo = %d, want 11", lo)
	}
	// A cutoff past the size stops where Prune does: the newest record
	// keeps its body.
	if n, err := c.ExpireBodies(stg, auditID, 1<<63); err != nil || n != 4 {
		t.Fatalf("ExpireBodies(past the size) = %d, %v, want 4", n, err)
	}
	final, _ := ReadHead(stg, auditID)
	if final.BodiesFrom != 9 || !bytes.Equal(final.Root, before.Root) {
		t.Fatalf("head after expiring every body = %+v", final)
	}
	if n := countPrefix(t, stg, string(logPrefix(auditID))+"n/"); n == 0 {
		t.Fatal("expiry removed tree nodes")
	}
}

func TestAuditInstance_DeterministicAcrossBatchPacking(t *testing.T) {
	run := func(pack int) string {
		stg := memory.NewStorage()
		c := NewChain()
		b := &batch{}
		ops := 0
		flush := func() {
			ops++
			if ops%pack == 0 {
				b.commit(t, stg)
				c.BeginUpdate()
			}
		}
		c.BeginUpdate()
		for n := range 4 {
			if _, err := c.Append(b, auditTenant, linked(n)); err != nil {
				t.Fatal(err)
			}
			flush()
		}
		for n := 4; n < 20; n++ {
			if _, err := c.AppendAudit(b, auditTenant, linked(n)); err != nil {
				t.Fatal(err)
			}
			flush()
		}
		if _, err := c.Prune(b, auditTenant, 8); err != nil {
			t.Fatal(err)
		}
		b.commit(t, stg)
		return dump(t, stg)
	}
	want := run(1)
	for _, pack := range []int{2, 7, 100} {
		if got := run(pack); got != want {
			t.Fatalf("state differs when %d commands share a batch", pack)
		}
	}
}
