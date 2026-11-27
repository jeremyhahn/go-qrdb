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

package main

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestAdminBinding_GetTopology_Success proves the desktop Topology view's
// call (wails.ts AdminBinding.GetTopology) reaches the admin service and
// returns its topology.
func TestAdminBinding_GetTopology_Success(t *testing.T) {
	admin := newMockAdminService()
	admin.topology = &service.TopologyResponse{
		NodeID:      "node-1",
		RaftAddress: "127.0.0.1:7000",
		ShardCount:  3,
	}
	conn := newTestConnectionBinding(t, newMockKVService(), admin)
	b := NewAdminBinding(testLogger(t), conn)

	resp, err := b.GetTopology()
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a non-nil TopologyResponse")
	}
	if resp.NodeID != "node-1" || resp.RaftAddress != "127.0.0.1:7000" || resp.ShardCount != 3 {
		t.Errorf("topology %+v, want node-1 at 127.0.0.1:7000 with 3 shards", resp)
	}
}

func TestAdminBinding_GetTopology_ServiceError(t *testing.T) {
	conn := newTestConnectionBinding(t, newMockKVService(), &errorAdminService{})
	b := NewAdminBinding(testLogger(t), conn)

	resp, err := b.GetTopology()
	if err == nil {
		t.Fatal("expected error")
	}
	if resp != nil {
		t.Fatal("expected nil response on service error")
	}
}

func TestAdminBinding_GetTopology_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewAdminBinding(testLogger(t), conn)

	_, err := b.GetTopology()
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got: %T: %v", err, err)
	}
}
