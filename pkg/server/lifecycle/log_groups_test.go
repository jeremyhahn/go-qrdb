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

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeGroupServices is a TenantHomeServices: shards maps tenant/db to the
// group's shard, a sealed key refuses with seal.ErrSealed.
type fakeGroupServices struct {
	shards  map[string]uint64
	sealed  map[string]bool
	unknown map[string]bool
	joining map[uint64]bool
	asked   []string
}

func (f *fakeGroupServices) Services(_ context.Context, tenantID, db string) (*server.DatabaseServices, error) {
	key := tenantID + "/" + db
	f.asked = append(f.asked, key)
	if f.sealed[key] {
		return nil, &server.RaftBackendError{Op: "resolve", Err: seal.ErrSealed}
	}
	if f.unknown[key] {
		return nil, &server.RaftBackendError{Op: "db-info", Err: server.ErrRaftDatabaseUnknown}
	}
	return &server.DatabaseServices{ShardID: f.shards[key]}, nil
}

func (f *fakeGroupServices) IsShardJoining(shardID uint64) bool { return f.joining[shardID] }

// groupHost is the node's host as the groups hand it out; only its identity
// is checked.
type groupHost struct{ service.Host }

// fakeGroupBarrier is a database barrier that is sealed or not.
type fakeGroupBarrier struct{ sealed bool }

func (b fakeGroupBarrier) Encrypt(dst, p []byte) ([]byte, error) { return append(dst, p...), nil }
func (b fakeGroupBarrier) Decrypt(dst, c []byte) ([]byte, error) { return append(dst, c...), nil }
func (b fakeGroupBarrier) IsSealed() bool                        { return b.sealed }

// fakeGroupBarriers maps a database name to its barrier.
type fakeGroupBarriers map[string]database.BarrierEncryptor

func (m fakeGroupBarriers) GetBarrier(dbName, _ string) database.BarrierEncryptor { return m[dbName] }

func TestLogGroups_TenantHomeGroup(t *testing.T) {
	ctx := context.Background()
	g := &logGroups{host: &groupHost{}}
	var unavailable *service.BarrierStoreUnavailableError
	if _, _, err := g.TenantHomeGroup(ctx, "vault"); !errors.As(err, &unavailable) || unavailable.TenantID != "vault" {
		t.Fatalf("unattached: err = %v, want *service.BarrierStoreUnavailableError", err)
	}
	svcs := &fakeGroupServices{
		shards:  map[string]uint64{"vault/" + database.TenantHomeName: 40, "late/" + database.TenantHomeName: 41},
		sealed:  map[string]bool{"locked/" + database.TenantHomeName: true},
		joining: map[uint64]bool{41: true},
	}
	g.attach(svcs)
	host, shardID, err := g.TenantHomeGroup(ctx, "vault")
	if err != nil || shardID != 40 || host != g.host {
		t.Fatalf("TenantHomeGroup(vault) = %v, %d, %v; want the home group 40", host, shardID, err)
	}
	if _, _, err := g.TenantHomeGroup(ctx, "locked"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("sealed: err = %v, want tenant.ErrTenantSealed", err)
	}
	var catching *TenantHomeCatchingUpError
	if _, _, err := g.TenantHomeGroup(ctx, "late"); !errors.As(err, &catching) {
		t.Fatalf("joining: err = %v, want *TenantHomeCatchingUpError", err)
	}
}

func TestLogGroups_DatabaseGroup(t *testing.T) {
	ctx := context.Background()
	barriers := fakeGroupBarriers{"secure": fakeGroupBarrier{}, "locked": fakeGroupBarrier{sealed: true}}
	g := &logGroups{host: &groupHost{}, barriers: func() logGroupBarriers { return barriers }}
	var unavailable *service.BarrierStoreUnavailableError
	if _, _, err := g.DatabaseGroup(ctx, "acme", "secure"); !errors.As(err, &unavailable) || unavailable.Database != "secure" {
		t.Fatalf("unattached: err = %v, want *service.BarrierStoreUnavailableError", err)
	}
	svcs := &fakeGroupServices{shards: map[string]uint64{"acme/secure": 50}}
	g.attach(svcs)
	host, shardID, err := g.DatabaseGroup(ctx, "acme", "secure")
	if err != nil || shardID != 50 || host != g.host {
		t.Fatalf("DatabaseGroup(acme, secure) = %v, %d, %v; want the database group 50", host, shardID, err)
	}
	svcs.asked = nil
	for _, db := range []string{"locked", "unknown"} {
		var sealed *database.SealedError
		if _, _, err := g.DatabaseGroup(ctx, "acme", db); !errors.As(err, &sealed) {
			t.Fatalf("%s: err = %v, want *database.SealedError", db, err)
		}
	}
	if len(svcs.asked) != 0 {
		t.Fatalf("a sealed database's group was opened: %v", svcs.asked)
	}
	svcs.unknown = map[string]bool{"acme/secure": true}
	if _, _, err := g.DatabaseGroup(ctx, "acme", "secure"); !errors.As(err, &unavailable) || unavailable.Database != "secure" {
		t.Fatalf("no dedicated group: err = %v, want *service.BarrierStoreUnavailableError", err)
	}
	svcs.unknown = nil
	svcs.sealed = map[string]bool{"acme/secure": true}
	var sealed *database.SealedError
	if _, _, err := g.DatabaseGroup(ctx, "acme", "secure"); !errors.As(err, &sealed) {
		t.Fatalf("group sealed: err = %v, want *database.SealedError", err)
	}
}
