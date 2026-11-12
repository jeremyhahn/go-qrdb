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

package main

import (
	"bytes"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// newTestVlogBinding returns a VlogBinding over an embedded transport with a
// working log service.
func newTestVlogBinding(t *testing.T) *VlogBinding {
	t.Helper()
	conn := newTestConnectionBinding(t, &noopKVService{}, newMockAdminService(),
		embedded.WithLogService(servicetest.NewLogService(t)))
	return NewVlogBinding(testLogger(t), conn)
}

// TestVlogBinding_Lifecycle drives a log through every VlogBinding method.
func TestVlogBinding_Lifecycle(t *testing.T) {
	b := newTestVlogBinding(t)

	created, err := b.Create(&vlogwire.CreateRequest{
		Log:               "events",
		Origin:            "example.com/events",
		AllowExpireBodies: true,
		SigningKeyType:    vlog.SignatureEd25519,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Log != "events" || created.Key == nil || len(created.Key.PublicKey) == 0 {
		t.Fatalf("Create = %+v; want log events with a head key", created)
	}

	list, err := b.List()
	if err != nil || len(list.Logs) != 1 || list.Logs[0] != "events" {
		t.Fatalf("List = %+v, %v; want [events]", list, err)
	}

	appended, err := b.Append(&vlogwire.AppendRequest{Log: "events", Entries: [][]byte{[]byte("a"), []byte("b"), []byte("c")}})
	if err != nil || appended.FirstIndex != 0 || appended.Count != 3 {
		t.Fatalf("Append = %+v, %v; want first 0 count 3", appended, err)
	}

	got, err := b.Get(&vlogwire.GetRequest{Log: "events", From: 1, Limit: 2})
	if err != nil || len(got.Entries) != 2 || !bytes.Equal(got.Entries[0].Body, []byte("b")) {
		t.Fatalf("Get = %+v, %v; want entries b, c", got, err)
	}

	head, err := b.Head(&vlogwire.LogRequest{Log: "events"})
	if err != nil || head.Size != 3 || len(head.Root) == 0 {
		t.Fatalf("Head = %+v, %v; want size 3 with a root", head, err)
	}

	inc, err := b.ProveInclusion(&vlogwire.InclusionRequest{Log: "events", Index: 1, Size: 3})
	if err != nil || inc.Index != 1 || inc.Size != 3 || len(inc.Hashes) == 0 {
		t.Fatalf("ProveInclusion = %+v, %v; want a proof for leaf 1 of 3", inc, err)
	}

	con, err := b.ProveConsistency(&vlogwire.ConsistencyRequest{Log: "events", Size1: 1, Size2: 3})
	if err != nil || con.Size1 != 1 || con.Size2 != 3 || len(con.Hashes) == 0 {
		t.Fatalf("ProveConsistency = %+v, %v; want a proof from 1 to 3", con, err)
	}

	signed, err := b.SignHead(&vlogwire.LogRequest{Log: "events"})
	if err != nil || signed.Head.Size != 3 || len(signed.Checkpoint) == 0 {
		t.Fatalf("SignHead = %+v, %v; want a signed head of size 3 and a checkpoint", signed, err)
	}

	rotated, err := b.RotateKey(&vlogwire.RotateKeyRequest{Log: "events", SigningKeyType: vlog.SignatureEd25519})
	if err != nil || rotated.Key.Version <= created.Key.Version {
		t.Fatalf("RotateKey = %+v, %v; want a version above %d", rotated, err, created.Key.Version)
	}

	expired, err := b.ExpireBodies(&vlogwire.ExpireRequest{Log: "events", Cutoff: 2})
	if err != nil || expired.Expired != 2 {
		t.Fatalf("ExpireBodies = %+v, %v; want 2 expired", expired, err)
	}
}

// TestVlogBinding_ServiceError returns the service's error for a log that
// does not exist.
func TestVlogBinding_ServiceError(t *testing.T) {
	b := newTestVlogBinding(t)
	if _, err := b.Head(&vlogwire.LogRequest{Log: "missing"}); err == nil {
		t.Fatal("Head of a missing log succeeded")
	}
}
