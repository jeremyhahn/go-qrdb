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

package backend_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/raftadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

func newLocalBackend(t *testing.T) *backend.LocalBackend {
	t.Helper()
	lb := backend.NewLocalBackend(engine.NewKVEngine(memory.NewStorage()))
	t.Cleanup(func() { _ = lb.Close() })
	return lb
}

// LocalBackend is a whole node host: the data host the data services take and
// the Raft-admin host the admin services take. The assertions live in an
// external test package because pkg/backend does not import the service layer
// it serves.
var (
	_ service.Host   = (*backend.LocalBackend)(nil)
	_ raftadmin.Host = (*backend.LocalBackend)(nil)
)

// TestLocalBackendServesBothHosts drives one LocalBackend through each half:
// its identity through service.Host, and its one logical shard and no-op
// drain through raftadmin.Host.
func TestLocalBackendServesBothHosts(t *testing.T) {
	lb := newLocalBackend(t)

	var data service.Host = lb
	if data.ID() == "" {
		t.Fatal("data host ID is empty")
	}
	var admin raftadmin.Host = lb
	if got := admin.ShardCount(); got != 1 {
		t.Fatalf("ShardCount = %d, want 1", got)
	}
	if err := admin.Drain(); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if admin.IsDraining() {
		t.Fatal("a LocalBackend, which has no Raft host to drain, reports draining")
	}
}

// TestLocalBackendAdminHostIsTheDataHost checks that the composed
// service.AdminHost a node is built on is the same LocalBackend value seen
// through either half.
func TestLocalBackendAdminHostIsTheDataHost(t *testing.T) {
	lb := newLocalBackend(t)

	var whole service.AdminHost = lb
	var data service.Host = whole
	var admin raftadmin.Host = whole
	if data.(*backend.LocalBackend) != lb || admin.(*backend.LocalBackend) != lb {
		t.Fatal("the AdminHost halves are not the LocalBackend they were built from")
	}
}
