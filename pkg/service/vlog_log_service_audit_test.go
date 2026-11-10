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

//go:build qrdb_vlog

package service

import (
	"encoding/json"
	"testing"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// TestLogService_ReadsTheAuditInstance pins that a caller holding the log
// surface reads its own tenant's audit instance (system/audit) like any named
// log: the head names the leaves appended since the switch and an inclusion
// proof of each verifies against it.
func TestLogService_ReadsTheAuditInstance(t *testing.T) {
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	var events [][]byte
	for i := range 3 {
		ev := fixedAuditEvent("acme")
		ev.Action = []string{"a", "b", "c"}[i]
		body, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, body)
	}
	if _, err := audit.ChainOn(vlog.NewChain()).AppendToInstance(store, "acme", events); err != nil {
		t.Fatalf("AppendToInstance: %v", err)
	}

	var routed []vlog.LogID
	svc := newTestLogService(t, readHost(func(q interface{}) (interface{}, error) {
		switch r := q.(type) {
		case vlog.HeadRequest:
			routed = append(routed, r.ID)
		case vlog.InclusionRequest:
			routed = append(routed, r.ID)
		}
		return vlog.Read(store, q.(vlog.Request))
	}), nil)
	ref := LogRef{Database: vlog.AuditDatabase, Log: vlog.AuditLog}

	head, err := svc.Head(acmeCtx(), &HeadRequest{LogRef: ref})
	if err != nil || head.Head.Size != 3 {
		t.Fatalf("Head = %+v, %v; want 3 leaves", head, err)
	}
	leaves, err := vlog.Get(store, vlog.AuditLogID("acme"), 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaf := range leaves {
		resp, err := svc.ProveInclusion(acmeCtx(), &InclusionRequest{LogRef: ref, Index: leaf.Index, Size: head.Head.Size})
		if err != nil {
			t.Fatalf("ProveInclusion(%d): %v", leaf.Index, err)
		}
		if err := proof.VerifyInclusion(rfc6962.DefaultHasher, leaf.Index, head.Head.Size,
			rfc6962.DefaultHasher.HashLeaf(leaf.Body), resp.Hashes, head.Head.Root); err != nil {
			t.Fatalf("VerifyInclusion(%d): %v", leaf.Index, err)
		}
	}
	for _, id := range routed {
		if id != vlog.AuditLogID("acme") {
			t.Fatalf("read routed to %v, want the caller's own audit instance", id)
		}
	}
}
