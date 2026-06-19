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

package server

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// C-1 — cross-tenant data exposure via nil/empty range lower bound
//
// These tests drive the namespaced wrappers through an inner servicer that
// stores keys from two namespaces and ranges over them with the exact
// pkg/mvcc.IsInRange semantics (nil start = start of the whole store, nil end =
// end of the whole store). Before the fix, a namespaced Range with a nil/empty
// start (and/or end) forwarded nil bounds to the inner store and returned the
// foreign namespace's keys; the strip step left those foreign keys intact. The
// tests assert the wrapper NEVER returns a foreign namespace's keys.
// ---------------------------------------------------------------------------

// seededMVCC is an inner MVCCKVServicer holding keys across namespaces. Its
// Range/RangeWithConsistency replicate pkg/mvcc.IsInRange so that an unconfined
// nil bound observably escapes the namespace, making the leak reproducible.
type seededMVCC struct {
	service.MVCCKVServicer
	store map[string][]byte
}

// inRange mirrors pkg/mvcc.IsInRange: [start, end) with nil start = open lower,
// nil end = open upper.
func inRange(key, start, end []byte) bool {
	if bytes.Compare(key, start) < 0 {
		return false
	}
	if end != nil && bytes.Compare(key, end) >= 0 {
		return false
	}
	return true
}

func (m *seededMVCC) scan(start, end []byte) []*service.KeyValue {
	keys := make([]string, 0, len(m.store))
	for k := range m.store {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []*service.KeyValue
	for _, k := range keys {
		kb := []byte(k)
		if inRange(kb, start, end) {
			out = append(out, &kv.KeyValue{Key: kb, Value: m.store[k]})
		}
	}
	return out
}

func (m *seededMVCC) Range(_ context.Context, start, end []byte, _ ...service.RangeOption) ([]*service.KeyValue, int64, error) {
	out := m.scan(start, end)
	return out, int64(len(out)), nil
}

func (m *seededMVCC) RangeWithConsistency(_ context.Context, start, end []byte, _ consistency.ReadOptions, _ ...service.RangeOption) ([]*service.KeyValue, int64, error) {
	out := m.scan(start, end)
	return out, int64(len(out)), nil
}

func newTwoTenantStore() *seededMVCC {
	return &seededMVCC{store: map[string][]byte{
		"tenantA/db/k1": []byte("a1"),
		"tenantA/db/k2": []byte("a2"),
		"tenantB/db/k1": []byte("b1"),
		"tenantB/db/k2": []byte("b2"),
	}}
}

func TestNamespacedMVCC_RangeNeverLeaksForeignNamespace(t *testing.T) {
	inner := newTwoTenantStore()
	wA := newNamespacedMVCCService(inner, "tenantA/db/")
	ctx := context.Background()

	bounds := []struct {
		name       string
		start, end []byte
	}{
		{"nil_start_nil_end", nil, nil},
		{"empty_start_empty_end", []byte{}, []byte{}},
		{"nil_start_explicit_end", nil, []byte("k9")},
		{"explicit_start_nil_end", []byte("k0"), nil},
	}

	assertOnlyTenantA := func(t *testing.T, kvs []*service.KeyValue) {
		t.Helper()
		got := map[string]bool{}
		for _, k := range kvs {
			ks := string(k.Key)
			if strings.Contains(ks, "tenantB") {
				t.Fatalf("CROSS-TENANT LEAK: foreign namespace key returned: %q", ks)
			}
			got[ks] = true
		}
		// tenantA's own keys (prefix-stripped) must still be returned, proving
		// the assertion is not vacuously satisfied by an empty result.
		if !got["k1"] || !got["k2"] {
			t.Fatalf("expected tenantA keys k1,k2 (stripped), got %v", got)
		}
		if len(got) != 2 {
			t.Fatalf("expected exactly 2 keys, got %v", got)
		}
	}

	for _, b := range bounds {
		t.Run("Range/"+b.name, func(t *testing.T) {
			kvs, _, err := wA.Range(ctx, b.start, b.end)
			if err != nil {
				t.Fatal(err)
			}
			assertOnlyTenantA(t, kvs)
		})
		t.Run("RangeWithConsistency/"+b.name, func(t *testing.T) {
			kvs, _, err := wA.RangeWithConsistency(ctx, b.start, b.end, consistency.ReadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			assertOnlyTenantA(t, kvs)
		})
	}
}

// opRec captures the bounds the namespaced batch/transaction wrappers forward to
// the inner servicer, so range-op lower/upper bound confinement can be asserted.
type opRec struct {
	service.BatchServicer
	keys     []string
	rangeEnd []string
	cmpKeys  []string
}

func (m *opRec) record(ops []service.Op) {
	for _, op := range ops {
		m.keys = append(m.keys, string(op.Key))
		m.rangeEnd = append(m.rangeEnd, string(op.RangeEnd))
	}
}

func (m *opRec) Execute(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
	m.record(ops)
	return nil, nil
}

func (m *opRec) txExecute(cmp []service.Compare, ok []service.Op) {
	for _, c := range cmp {
		m.cmpKeys = append(m.cmpKeys, string(c.Key))
	}
	m.record(ok)
}

// executeTxn records a transaction's compares and success ops. A distinct name
// avoids colliding with the batch Execute above.
func (m *opRec) executeTxn(cmp []service.Compare, ok []service.Op) (*service.TransactionResponse, error) {
	m.txExecute(cmp, ok)
	return &service.TransactionResponse{}, nil
}

func TestNamespacedBatch_RangeOpLowerBoundConfined(t *testing.T) {
	rec := &opRec{}
	w := newNamespacedBatchService(rec, "tenantA/db/")
	// A range op (OpRange) with an absent lower bound and absent end. Before the
	// fix the lower bound stayed nil (whole-store start) and the end stayed nil
	// (whole-store end), scanning every namespace.
	if _, err := w.Execute(context.Background(), []service.Op{{Type: service.OpRange}}); err != nil {
		t.Fatal(err)
	}
	if rec.keys[0] != "tenantA/db/" {
		t.Errorf("range-op lower bound = %q, want confined to %q", rec.keys[0], "tenantA/db/")
	}
	if rec.rangeEnd[0] != "tenantA/db0" {
		t.Errorf("range-op upper bound = %q, want namespace successor %q", rec.rangeEnd[0], "tenantA/db0")
	}
}

func TestNamespacedBatch_SingleKeyOpNilEndPreserved(t *testing.T) {
	rec := &opRec{}
	w := newNamespacedBatchService(rec, "tenantA/db/")
	// A single-key put must keep a nil RangeEnd so it is not misread as a range.
	if _, err := w.Execute(context.Background(), []service.Op{{Type: service.OpPut, Key: []byte("k"), Value: []byte("v")}}); err != nil {
		t.Fatal(err)
	}
	if rec.keys[0] != "tenantA/db/k" {
		t.Errorf("single-key op key = %q", rec.keys[0])
	}
	if rec.rangeEnd[0] != "" {
		t.Errorf("single-key op RangeEnd = %q, want empty (nil preserved)", rec.rangeEnd[0])
	}
}

func TestNamespacedTransaction_RangeOpAndNilCompareConfined(t *testing.T) {
	rec := &opRec{}
	w := newNamespacedTransactionService(txnAdapter{rec}, "tenantA/db/")
	// A nil compare key and a delete-range op with an absent lower bound.
	_, err := w.Execute(context.Background(),
		[]service.Compare{{Key: nil}},
		[]service.Op{{Type: service.OpDelete, RangeEnd: []byte("z")}},
		nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec.cmpKeys[0] != "tenantA/db/" {
		t.Errorf("nil compare key = %q, want confined to namespace prefix", rec.cmpKeys[0])
	}
	if rec.keys[0] != "tenantA/db/" {
		t.Errorf("range-op lower bound = %q, want confined to namespace prefix", rec.keys[0])
	}
	if rec.rangeEnd[0] != "tenantA/db/z" {
		t.Errorf("range-op upper bound = %q, want prefixed end", rec.rangeEnd[0])
	}
}

// txnAdapter adapts opRec to the TransactionServicer interface.
type txnAdapter struct{ rec *opRec }

func (a txnAdapter) Execute(_ context.Context, cmp []service.Compare, ok, _ []service.Op) (*service.TransactionResponse, error) {
	return a.rec.executeTxn(cmp, ok)
}

func (a txnAdapter) ExecuteWithMode(_ context.Context, cmp []service.Compare, ok, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return a.rec.executeTxn(cmp, ok)
}

func (a txnAdapter) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return &service.TransactionEntityResponse{}, nil
}
