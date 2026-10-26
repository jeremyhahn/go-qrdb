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
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// fakeShardHost is a Raft host whose shards are IDs: it records which it was
// asked to pause and resume, and fails a pause with pauseErr[id].
type fakeShardHost struct {
	shards   []uint64
	pauseErr map[uint64]error
	// onPause runs inside every PauseShard, before it records the shard.
	onPause func()

	mu      sync.Mutex
	paused  []uint64
	resumed []uint64
}

var _ shardHost = (*fakeShardHost)(nil)

func (f *fakeShardHost) NodeHostInfo(quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo {
	info := &quicraft.NodeHostInfo{}
	for _, id := range f.shards {
		info.ShardInfoList = append(info.ShardInfoList, quicraft.ShardInfo{ShardID: id})
	}
	return info
}

func (f *fakeShardHost) PauseShard(_ context.Context, shardID uint64) error {
	if f.onPause != nil {
		f.onPause()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = append(f.paused, shardID)
	return f.pauseErr[shardID]
}

func (f *fakeShardHost) ResumeShard(_ context.Context, shardID uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, shardID)
	return nil
}

// sortedPaused and sortedResumed return what the host was asked to pause and
// resume, sorted: the pauser pauses concurrently and resumes in map order.
func (f *fakeShardHost) sortedPaused() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(slices.Values(f.paused))
}

func (f *fakeShardHost) sortedResumed() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(slices.Values(f.resumed))
}

// fakeVolumeHosts lists volume tenants' hosts and records which it closed.
type fakeVolumeHosts struct {
	fakeHosts
	hosts map[string]shardHost
}

var _ volumeTenantHosts = (*fakeVolumeHosts)(nil)

func (f *fakeVolumeHosts) shardHosts() map[string]shardHost { return f.hosts }

// sealNode wipes the node's keys the way a whole-node seal does.
func sealNode(t *testing.T, p *ShardPauser, reg *seal.BarrierRegistry) {
	t.Helper()
	if err := service.SealWholeNode(context.Background(), p, reg, nil, nil); err != nil {
		t.Fatalf("SealWholeNode: %v", err)
	}
}

// unsealNode restores the node barrier's key, which runs its observers.
func unsealNode(t *testing.T, reg *seal.BarrierRegistry) {
	t.Helper()
	if err := reg.SystemBarrier().Unseal(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
}

// registerAutoTenant registers and initializes tenantID with a system-managed
// barrier: its key is wrapped by the node barrier and comes back with it.
func registerAutoTenant(t *testing.T, reg *seal.BarrierRegistry, tenantID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := reg.RegisterTenant(ctx, tenantID, seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "tenants/" + tenantID + "/root-key"},
		seal.NewSystemBarrierStrategy(reg.SystemBarrier())); err != nil {
		t.Fatalf("RegisterTenant(%s): %v", tenantID, err)
	}
	if err := reg.InitializeTenant(ctx, tenantID, seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(%s): %v", tenantID, err)
	}
}

func TestNewShardPauser_NilPartsPauseNothing(t *testing.T) {
	if p := NewShardPauser(nil, newEnforcerRegistry(t), nil); p != nil {
		t.Fatalf("NewShardPauser(nil host) = %v, want nil", p)
	}
	if p := newShardPauser(&fakeShardHost{}, nil, nil); p != nil {
		t.Fatalf("newShardPauser(nil registry) = %v, want nil", p)
	}
	var p *ShardPauser
	if err := p.PauseShards(context.Background()); err != nil {
		t.Fatalf("nil PauseShards = %v", err)
	}
	p.SetTenants(nil, nil)
}

func TestShardPauser_PausesEveryHostsShardsAndResumesOnUnseal(t *testing.T) {
	reg := newEnforcerRegistry(t)
	registerAutoTenant(t, reg, "initech")
	node := &fakeShardHost{shards: []uint64{shard.ShardKV, 7}}
	volume := &fakeShardHost{shards: []uint64{9}}
	p := newShardPauser(node, reg, nil)
	p.tenants.Store(&pauserTenants{hosts: &fakeVolumeHosts{hosts: map[string]shardHost{"initech": volume}}})

	sealNode(t, p, reg)
	if got, want := node.sortedPaused(), []uint64{shard.ShardKV, 7}; !slices.Equal(got, want) {
		t.Fatalf("node host paused %v, want %v", got, want)
	}
	if got := volume.sortedPaused(); !slices.Equal(got, []uint64{9}) {
		t.Fatalf("volume host paused %v, want [9]", got)
	}
	if len(node.sortedResumed())+len(volume.sortedResumed()) != 0 {
		t.Fatal("a shard resumed while the node is sealed")
	}

	unsealNode(t, reg)
	if got, want := node.sortedResumed(), []uint64{shard.ShardKV, 7}; !slices.Equal(got, want) {
		t.Fatalf("node host resumed %v, want %v", got, want)
	}
	if got := volume.sortedResumed(); !slices.Equal(got, []uint64{9}) {
		t.Fatalf("volume host resumed %v, want [9] (initech auto-unseals with the node)", got)
	}

	// The resume consumed the record: a second unseal resumes nothing more.
	if err := reg.SystemBarrier().Seal(); err != nil {
		t.Fatal(err)
	}
	unsealNode(t, reg)
	if got := node.sortedResumed(); len(got) != 2 {
		t.Fatalf("a later unseal resumed again: %v", got)
	}
}

// TestShardPauser_ResumesWhenTheReplacementSystemBarrierUnseals proves the
// pauser's resume follows the system barrier when the encryption migration
// replaces it: a node sealed after the swap resumes its shards when the
// replacement unseals, not only when the barrier it was wired to does.
func TestShardPauser_ResumesWhenTheReplacementSystemBarrierUnseals(t *testing.T) {
	reg := newEnforcerRegistry(t)
	node := &fakeShardHost{shards: []uint64{shard.ShardKV, 7}}
	p := newShardPauser(node, reg, nil)

	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("replacement-pass"))
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)),
		seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "sys/root"}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	reg.SetSystemBarrier(replacement)

	sealNode(t, p, reg)
	if got, want := node.sortedPaused(), []uint64{shard.ShardKV, 7}; !slices.Equal(got, want) {
		t.Fatalf("node host paused %v, want %v", got, want)
	}
	unsealNode(t, reg)
	if got, want := node.sortedResumed(), []uint64{shard.ShardKV, 7}; !slices.Equal(got, want) {
		t.Fatalf("after the replacement unsealed the node host resumed %v, want %v", got, want)
	}
}

func TestShardPauser_PausesBeforeTheKeysGo(t *testing.T) {
	reg := newEnforcerRegistry(t)
	var unsealedAtPause bool
	node := &fakeShardHost{shards: []uint64{shard.ShardKV}}
	node.onPause = func() { unsealedAtPause = !reg.SystemBarrier().IsSealed() }
	p := newShardPauser(node, reg, nil)

	sealNode(t, p, reg)
	if !unsealedAtPause {
		t.Fatal("the shard was paused after the node key was wiped")
	}
	if !reg.SystemBarrier().IsSealed() {
		t.Fatal("the node key survived the seal")
	}
}

func TestShardPauser_JoinsPauseFailuresAndIgnoresStoppedShards(t *testing.T) {
	reg := newEnforcerRegistry(t)
	broken := errors.New("pause refused")
	node := &fakeShardHost{
		shards: []uint64{1, 2, 3, 4},
		pauseErr: map[uint64]error{
			2: quicraft.ErrShardNotFound,
			3: quicraft.ErrClosed,
			4: broken,
		},
	}
	p := newShardPauser(node, reg, nil)

	err := p.PauseShards(context.Background())
	if !errors.Is(err, broken) {
		t.Fatalf("PauseShards = %v, want %v", err, broken)
	}
	if errors.Is(err, quicraft.ErrShardNotFound) || errors.Is(err, quicraft.ErrClosed) {
		t.Fatalf("PauseShards reported a stopped shard: %v", err)
	}

	// A failed pause still seals: the keys go whatever the pause returned.
	if err := service.SealWholeNode(context.Background(), p, reg, nil, nil); !errors.Is(err, broken) {
		t.Fatalf("SealWholeNode = %v, want %v", err, broken)
	}
	if !reg.SystemBarrier().IsSealed() {
		t.Fatal("a failed pause kept the node key")
	}
}

func TestShardPauser_ClosesAStillSealedTenantInsteadOfResumingIt(t *testing.T) {
	reg := newEnforcerRegistry(t)
	// acme's barrier is self-managed, so the node unseal leaves it sealed;
	// globex is system-managed and comes back with the node.
	registerAutoTenant(t, reg, "globex")
	node := &fakeShardHost{shards: []uint64{shard.ShardKV}}
	acmeHost := &fakeShardHost{shards: []uint64{11, 12}}
	globexHost := &fakeShardHost{shards: []uint64{21}}
	shards := &fakeShards{started: []StartedShard{
		{TenantID: "acme", Database: "orders"}, {TenantID: "globex", Database: "orders"},
	}}
	hosts := &fakeVolumeHosts{hosts: map[string]shardHost{"acme": acmeHost, "globex": globexHost}}
	p := newShardPauser(node, reg, nil)
	p.tenants.Store(&pauserTenants{shards: shards, hosts: hosts})

	sealNode(t, p, reg)
	unsealNode(t, reg)

	if want := []StartedShard{{TenantID: "acme", Database: "orders"}}; !slices.Equal(shards.closed, want) {
		t.Fatalf("closed shards = %v, want %v", shards.closed, want)
	}
	if !slices.Equal(hosts.closed, []string{"acme"}) {
		t.Fatalf("closed hosts = %v, want [acme]", hosts.closed)
	}
	if got := acmeHost.sortedResumed(); len(got) != 0 {
		t.Fatalf("a sealed tenant's host resumed %v", got)
	}
	if got := globexHost.sortedResumed(); !slices.Equal(got, []uint64{21}) {
		t.Fatalf("globex resumed %v, want [21]", got)
	}
	if got := node.sortedResumed(); !slices.Equal(got, []uint64{shard.ShardKV}) {
		t.Fatalf("node host resumed %v", got)
	}
}

func TestShardPauser_SetTenantsStoresOnlyThePartsGiven(t *testing.T) {
	reg := newEnforcerRegistry(t)
	p := newShardPauser(&fakeShardHost{}, reg, nil)

	p.SetTenants(nil, nil)
	if got := p.tenants.Load(); got == nil || got.shards != nil || got.hosts != nil {
		t.Fatalf("SetTenants(nil, nil) stored %+v, want empty parts", got)
	}

	p.SetTenants(nil, &TenantHostRegistry{})
	got := p.tenants.Load()
	if got.shards != nil {
		t.Fatal("a nil *RaftBackendFactory was stored as a non-nil tenantShards")
	}
	if got.hosts == nil {
		t.Fatal("the host registry was not stored")
	}
	if n := len(p.targets()); n != 0 {
		t.Fatalf("targets = %d, want 0", n)
	}
}

func TestTenantHostRegistry_ShardHostsListsOnlyBuiltHosts(t *testing.T) {
	host, _ := newSharedTestHost(t)
	r := &TenantHostRegistry{hosts: map[string]*tenantHost{"acme": {host: host}, "globex": {}}}

	got := r.shardHosts()
	if len(got) != 1 || got["acme"] != shardHost(host) {
		t.Fatalf("shardHosts = %v, want only acme", got)
	}
	if n := len((&TenantHostRegistry{}).shardHosts()); n != 0 {
		t.Fatalf("empty registry listed %d hosts", n)
	}
}

// resumeNode runs a whole-node unseal after a pause that never reached the
// wipe: the node key is dropped and restored, which runs its observers.
func resumeNode(t *testing.T, reg *seal.BarrierRegistry) {
	t.Helper()
	if err := reg.SystemBarrier().Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	unsealNode(t, reg)
}

// TestShardPauser_NoVolumeTenantHostStartsAfterThePause builds a volume
// tenant's host between the pause and the wipe, while the tenant's own
// barrier is still unsealed: the build is refused as sealed, and goes ahead
// once the node is unsealed.
func TestShardPauser_NoVolumeTenantHostStartsAfterThePause(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	registerVolumeTenant(t, reg, "vt")
	hosts := NewTenantHostRegistry(reg, &lookupDataRootSource{registry: reg}, volTestLogger())
	t.Cleanup(func() { _ = hosts.CloseAll() })
	p := newShardPauser(&fakeShardHost{}, reg, nil)
	p.SetTenants(nil, hosts)
	ctx := context.Background()

	if err := p.PauseShards(ctx); err != nil {
		t.Fatalf("PauseShards: %v", err)
	}
	if _, err := hosts.Services(ctx, "vt"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Services after the pause = %v, want %v", err, seal.ErrSealed)
	}
	if hosts.Started("vt") {
		t.Fatal("a volume tenant's host started after the pause")
	}

	resumeNode(t, reg)
	if _, err := hosts.Services(ctx, "vt"); err != nil {
		t.Fatalf("Services after the unseal: %v", err)
	}
}

// TestShardPauser_NoRaftTenantShardStartsAfterThePause starts a raft
// tenant's database shard between the pause and the wipe: the pause listed
// the host's shards before this one existed, so it would run unpaused. The
// start is refused as sealed, and goes ahead once the node is unsealed.
func TestShardPauser_NoRaftTenantShardStartsAfterThePause(t *testing.T) {
	f, reg, src := newRaftFactory(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "app", 1000, dataRoot)
	p := newShardPauser(&fakeShardHost{}, reg, nil)
	p.SetTenants(f, nil)
	ctx := context.Background()

	if err := p.PauseShards(ctx); err != nil {
		t.Fatalf("PauseShards: %v", err)
	}
	if _, err := f.Services(ctx, "rt", "app"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Services after the pause = %v, want %v", err, seal.ErrSealed)
	}
	if started := f.ListStarted(); len(started) != 0 {
		t.Fatalf("a raft tenant shard started after the pause: %v", started)
	}

	resumeNode(t, reg)
	if _, err := f.Services(ctx, "rt", "app"); err != nil {
		t.Fatalf("Services after the unseal: %v", err)
	}
}

func TestShardPauser_BeginStartAdmitsUntilThePauseAndAfterTheResume(t *testing.T) {
	var none *ShardPauser
	done, err := none.beginStart()
	if err != nil {
		t.Fatalf("nil beginStart = %v", err)
	}
	done()

	reg := newEnforcerRegistry(t)
	p := newShardPauser(&fakeShardHost{shards: []uint64{shard.ShardKV}}, reg, nil)
	done, err = p.beginStart()
	if err != nil {
		t.Fatalf("beginStart before the pause = %v", err)
	}
	done()

	sealNode(t, p, reg)
	if _, err := p.beginStart(); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("beginStart while sealed = %v, want %v", err, seal.ErrSealed)
	}
	unsealNode(t, reg)
	done, err = p.beginStart()
	if err != nil {
		t.Fatalf("beginStart after the unseal = %v", err)
	}
	done()
}

// TestShardPauser_PauseWaitsForAnAdmittedStart holds a start open across a
// pause: the pause does not list the hosts until the start is done, so the
// shard it started is among those paused.
func TestShardPauser_PauseWaitsForAnAdmittedStart(t *testing.T) {
	reg := newEnforcerRegistry(t)
	node := &fakeShardHost{}
	p := newShardPauser(node, reg, nil)
	done, err := p.beginStart()
	if err != nil {
		t.Fatalf("beginStart: %v", err)
	}

	paused := make(chan error, 1)
	go func() { paused <- p.PauseShards(context.Background()) }()
	select {
	case err := <-paused:
		t.Fatalf("PauseShards returned with a start in progress: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	node.shards = []uint64{7}
	done()
	if err := <-paused; err != nil {
		t.Fatalf("PauseShards: %v", err)
	}
	if got := node.sortedPaused(); !slices.Equal(got, []uint64{7}) {
		t.Fatalf("paused %v, want the shard the start brought up [7]", got)
	}
}

// TestShardPauser_WriteRacingTheSealNeverReachesTheLogAfterTheWipe races
// writers against a whole-node seal on a real Raft host: once the keys are
// wiped no entry reaches the system shard's log, every write is refused as
// paused, and the unseal brings the shard back to accept writes.
func TestShardPauser_WriteRacingTheSealNeverReachesTheLogAfterTheWipe(t *testing.T) {
	c := raftharness.NewCluster(t, 1)
	host := c.Hosts[0]
	reg := newEnforcerRegistry(t)
	p := NewShardPauser(host, reg, nil)
	ctx, cancel := context.WithTimeout(context.Background(), raftharness.Bound)
	defer cancel()
	put, err := kv.EncodePut([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	lastIndex := func() uint64 {
		t.Helper()
		diag, err := host.DiagRaftState(shard.ShardKV)
		if err != nil {
			t.Fatalf("DiagRaftState: %v", err)
		}
		return diag.LastIndex
	}
	if _, err := host.SyncPropose(ctx, shard.ShardKV, put); err != nil {
		t.Fatalf("write before the seal: %v", err)
	}

	stop := make(chan struct{})
	var writers sync.WaitGroup
	for range 4 {
		writers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = host.SyncPropose(ctx, shard.ShardKV, put)
			}
		})
	}

	sealNode(t, p, reg)
	wiped := lastIndex()
	for i := range 100 {
		if _, err := host.SyncPropose(ctx, shard.ShardKV, put); !errors.Is(err, quicraft.ErrShardPaused) {
			t.Fatalf("write %d after the wipe = %v, want %v", i, err, quicraft.ErrShardPaused)
		}
	}
	if got := lastIndex(); got != wiped {
		t.Fatalf("the log grew from %d to %d after the keys were wiped", wiped, got)
	}

	close(stop)
	unsealNode(t, reg)
	writers.Wait()
	c.WaitReadable(t, shard.ShardKV, host)
	if _, err := host.SyncPropose(ctx, shard.ShardKV, put); err != nil {
		t.Fatalf("write after the unseal: %v", err)
	}
	if got := lastIndex(); got <= wiped {
		t.Fatalf("the log did not advance after the unseal: %d <= %d", got, wiped)
	}
}
