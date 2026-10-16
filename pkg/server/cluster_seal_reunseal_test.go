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
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// reunsealKV is an in-memory clusterseal.KV.
type reunsealKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (k *reunsealKV) Get(_ context.Context, key string) ([]byte, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[key]
	return v, ok, nil
}
func (k *reunsealKV) PutIfAbsent(_ context.Context, key string, v []byte) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.m[key]; ok {
		return false, nil
	}
	k.m[key] = v
	return true, nil
}
func (k *reunsealKV) CompareAndSwap(_ context.Context, key string, exp, next []byte) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if string(k.m[key]) != string(exp) {
		return false, nil
	}
	k.m[key] = next
	return true, nil
}
func (k *reunsealKV) Put(_ context.Context, key string, v []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = v
	return nil
}
func (k *reunsealKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := map[string][]byte{}
	for key, v := range k.m {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			out[key] = v
		}
	}
	return out, nil
}

// ScanStale reads the same map: this single-process store has no quorum,
// so its local copy is always current.
func (k *reunsealKV) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return k.Scan(ctx, prefix)
}

// reunsealGate reports every feature activated.
type reunsealGate struct{}

func (reunsealGate) Activated(featuregate.Feature) bool { return true }
func (reunsealGate) ActivationsLoaded() bool            { return true }

// TestNodeUnseal_LeavesAClusterSealedTenantSealed (from the final review):
// a system-managed tenant sealed cluster-wide by its own record before a node
// seal stays sealed when the node is unsealed and its record cleared -- the
// node seal did not move it, so the node unseal must not unseal it; otherwise
// it would hold its key while its record says sealed, a record the tenant
// admin could then never clear.
func TestNodeUnseal_LeavesAClusterSealedTenantSealed(t *testing.T) {
	ctx := context.Background()
	reg := newEnforcerRegistry(t)
	sys := reg.SystemBarrier()
	if _, err := reg.RegisterTenant(ctx, "auto", seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "tenants/auto/root"}, seal.NewSystemBarrierStrategy(sys)); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitializeTenant(ctx, "auto", seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	m, err := clusterseal.NewMonitor(clusterseal.Config{
		KV: &reunsealKV{m: map[string][]byte{}}, Enforcer: &SealEnforcer{Barriers: reg}, Self: "1",
		Members: func(context.Context) ([]string, error) { return []string{"1"}, nil }, Gate: reunsealGate{},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Run(cancelledCtx())
	if _, err := m.Seal(ctx, clusterseal.TenantBarrier("auto"), clusterseal.Principal{Name: "t-admin", Tenant: "auto"}); err != nil {
		t.Fatal(err)
	}
	tb, _ := reg.GetTenant("auto")
	t.Logf("after cluster tenant seal: auto sealed=%v", tb.IsSealed())
	if _, err := m.Seal(ctx, clusterseal.NodeBarrier(), clusterseal.Principal{Name: "op"}); err != nil {
		t.Fatal(err)
	}
	t.Logf("after cluster node seal: sys sealed=%v auto sealed=%v", sys.IsSealed(), tb.IsSealed())
	if err := sys.Unseal(ctx, seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Clear(ctx, clusterseal.NodeBarrier(), clusterseal.Principal{Name: "op"}); err != nil {
		t.Fatal(err)
	}
	m.Run(cancelledCtx())
	if !m.Refuses(clusterseal.TenantBarrier("auto")) {
		t.Fatal("precondition: the tenant's record no longer refuses it")
	}
	if !tb.IsSealed() {
		t.Fatal("a tenant whose cluster record is sealed holds its key again after the node unseal")
	}
	// The tenant's admin can still unseal it -- the step that clears its
	// record -- rather than finding it unsealed already.
	if err := reg.UnsealTenant(ctx, "auto", seal.Credentials{}); err != nil {
		t.Fatalf("the tenant admin's unseal: %v (already unsealed: %v)", err, errors.Is(err, seal.ErrAlreadyUnsealed))
	}
}

// cancelledCtx makes Monitor.Run make one read and return.
func cancelledCtx() context.Context {
	c, cancel := context.WithCancel(context.Background())
	cancel()
	return c
}
