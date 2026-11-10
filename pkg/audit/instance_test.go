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

package audit

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

func marshaledEvent(t *testing.T, n int, tenantID string) []byte {
	t.Helper()
	b, err := json.Marshal(fixedEvent(n, tenantID))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestChain_AppendToInstance_ContinuesTheChain switches a tenant's chain to
// its audit instance mid-stream: leaf 0 is the event at S₀ carrying the
// pre-switch digest, every later event is a leaf, Verify stays continuous
// across the switch and after a prune, and the leaves' proofs verify.
func TestChain_AppendToInstance_ContinuesTheChain(t *testing.T) {
	const tenantID = "acme"
	c := NewChain()
	store := memory.NewStorage()
	id := vlog.AuditLogID(tenantID)

	for i := range 3 {
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_, _, pre := c.log.Head(tenantID)

	seq, err := c.AppendToInstance(store, tenantID, [][]byte{marshaledEvent(t, 3, tenantID), marshaledEvent(t, 4, tenantID)})
	if err != nil || seq != 5 {
		t.Fatalf("AppendToInstance = %d, %v, want 5", seq, err)
	}
	desc, err := vlog.ReadDescriptor(store, id)
	if err != nil || desc.TreeStart != 4 {
		t.Fatalf("descriptor = %+v, %v, want TreeStart 4", desc, err)
	}
	leaves, err := vlog.Get(store, id, 0, 2)
	if err != nil || len(leaves) != 2 {
		t.Fatalf("Get = %v, %v", leaves, err)
	}
	var first AuditEvent
	if err := json.Unmarshal(leaves[0].Body, &first); err != nil {
		t.Fatal(err)
	}
	if want := "sha256:" + hex.EncodeToString(pre[:]); first.PrevHash != want {
		t.Fatalf("leaf 0 PrevHash = %q, want the pre-switch head %q", first.PrevHash, want)
	}

	// An OpAuditAppend after the switch is the next leaf.
	for i := 5; i < 7; i++ {
		if _, err := c.Append(store, tenantID, fixedEvent(i, tenantID)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	report, err := c.Verify(store, tenantID)
	if err != nil || !report.Verified || report.Records != 7 || !report.Genesis {
		t.Fatalf("Verify across the switch = %+v, %v", report, err)
	}

	head, err := vlog.ReadHead(store, id)
	if err != nil || head.Size != 4 {
		t.Fatalf("ReadHead = %+v, %v, want size 4", head, err)
	}
	all, err := vlog.Get(store, id, 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		p, err := vlog.Inclusion(store, id, e.Index, head.Size)
		if err != nil {
			t.Fatalf("Inclusion(%d): %v", e.Index, err)
		}
		if err := proof.VerifyInclusion(rfc6962.DefaultHasher, e.Index, head.Size, rfc6962.DefaultHasher.HashLeaf(e.Body), p, head.Root); err != nil {
			t.Fatalf("VerifyInclusion(%d): %v", e.Index, err)
		}
	}
	// Prune past S₀ is the instance's ExpireBodies: Verify still holds over
	// what survives, and the tree is untouched.
	if _, err := c.Prune(store, tenantID, 6); err != nil {
		t.Fatal(err)
	}
	report, err = c.Verify(store, tenantID)
	if err != nil || !report.Verified || report.Low != 6 {
		t.Fatalf("Verify after prune = %+v, %v", report, err)
	}
	after, err := vlog.ReadHead(store, id)
	if err != nil || !bytes.Equal(after.Root, head.Root) || after.BodiesFrom != 2 {
		t.Fatalf("head after prune = %+v, %v, want the same root and BodiesFrom 2", after, err)
	}
}

func TestChain_AppendToInstance_Refusals(t *testing.T) {
	const tenantID = "acme"
	zero := fixedEvent(0, tenantID)
	zero.Timestamp = time.Time{}
	zeroRaw, _ := json.Marshal(zero)
	var (
		malformed *MalformedEventError
		missing   *MissingTimestampError
		mismatch  *TenantMismatchError
		batch     *vlog.AppendBatchError
		badTenant *vlog.InvalidTenantIDError
	)
	for name, tc := range map[string]struct {
		tenant  string
		entries [][]byte
		target  any
	}{
		"malformed":       {tenantID, [][]byte{[]byte("{")}, &malformed},
		"zero timestamp":  {tenantID, [][]byte{zeroRaw}, &missing},
		"tenant mismatch": {tenantID, [][]byte{marshaledEvent(t, 1, "other")}, &mismatch},
		"empty batch":     {tenantID, nil, &batch},
		"slash in tenant": {"a/b", [][]byte{marshaledEvent(t, 1, "")}, &badTenant},
		"one bad of two":  {tenantID, [][]byte{marshaledEvent(t, 1, tenantID), []byte("x")}, &malformed},
	} {
		t.Run(name, func(t *testing.T) {
			c := NewChain()
			store := memory.NewStorage()
			_, err := c.AppendToInstance(store, tc.tenant, tc.entries)
			if err == nil || !errors.As(err, tc.target) {
				t.Fatalf("AppendToInstance = %v, want %T", err, tc.target)
			}
			if seq, _, _ := c.log.Head(tc.tenant); seq != 0 {
				t.Fatalf("a refused append advanced the chain to %d", seq)
			}
		})
	}
	if (&MalformedEventError{Err: errors.New("x")}).Error() == "" {
		t.Fatal("empty error text")
	}
}
