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
	"log/slog"
	"sync"
	"sync/atomic"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// shardHost is the part of *quicraft.Host the shard pauser drives.
type shardHost interface {
	GetNodeHostInfo(opt quicraft.NodeHostInfoOption) *quicraft.NodeHostInfo
	PauseShard(ctx context.Context, shardID uint64) error
	ResumeShard(ctx context.Context, shardID uint64) error
}

var _ shardHost = (*quicraft.Host)(nil)

// volumeTenantHosts lists the volume tenants' own Raft hosts and stops one.
type volumeTenantHosts interface {
	tenantHosts
	shardHosts() map[string]shardHost
}

var _ volumeTenantHosts = (*TenantHostRegistry)(nil)

// pauseKey names one paused replica: a shard on a host.
type pauseKey struct {
	host    shardHost
	shardID uint64
}

// pauserTenants are the tenant registries the pauser consults on resume,
// built after the pauser itself (SetTenants).
type pauserTenants struct {
	shards tenantShards
	hosts  volumeTenantHosts
}

// ShardPauser pauses every Raft shard this node hosts before a whole-node
// seal wipes the keys, and resumes them once the node barrier is unsealed
// again (docs/barrier-seal/architecture.md, "Sealing the node barrier seals
// the whole node"). The shards are those of the node's own host -- the
// system shard and every tenant shard on it -- and those of each volume
// tenant's host. A paused shard stays loaded: nothing is shut down, and a
// pause is not persisted, so a restarted node starts unpaused. From the pause
// until the resume no tenant shard or volume tenant host starts on the node
// (beginStart), so none comes up unpaused behind the pause's back.
//
// It resumes from the node barrier's OnUnsealed observer, which runs after
// the keys are restored and after the registry has unsealed the
// system-managed tenant barriers (registered before it). A tenant whose
// barrier is still sealed then has its shards and host closed instead, as a
// tenant seal does (SealEnforcer.SealTenant), so the tenant's unseal rebuilds
// them; every other paused shard resumes.
//
// The zero value is not usable; build one with NewShardPauser. A nil
// *ShardPauser pauses nothing.
type ShardPauser struct {
	host     shardHost
	barriers *seal.BarrierRegistry
	logger   *slog.Logger
	tenants  atomic.Pointer[pauserTenants]

	// mu serializes a pause against a resume, so a resume never misses a
	// replica a concurrent pause is still recording.
	mu sync.Mutex
	// paused maps each replica this pauser paused to the volume tenant
	// owning its host, "" for the node's own host.
	paused map[pauseKey]string

	// gate orders shard starts against a pause: a start holds it shared
	// (beginStart), and a pause or resume exclusively while it flips sealed.
	gate sync.RWMutex
	// sealed is set by a pause and cleared by the resume; while it is set no
	// shard starts.
	sealed bool
}

var _ service.ShardPauser = (*ShardPauser)(nil)

// NewShardPauser returns the pauser of host's shards, resuming them when
// barriers' node barrier is unsealed. It returns nil -- a pauser that pauses
// nothing -- when host, barriers or the node barrier is nil. Call SetTenants
// once the tenant registries exist.
func NewShardPauser(host *quicraft.Host, barriers *seal.BarrierRegistry, logger *slog.Logger) *ShardPauser {
	if host == nil {
		return nil
	}
	return newShardPauser(host, barriers, logger)
}

func newShardPauser(host shardHost, barriers *seal.BarrierRegistry, logger *slog.Logger) *ShardPauser {
	if barriers == nil || barriers.SystemBarrier() == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	p := &ShardPauser{host: host, barriers: barriers, logger: logger, paused: make(map[pauseKey]string)}
	barriers.SystemBarrier().OnUnsealed(p.resume)
	return p
}

// SetTenants gives the pauser the tenant raft shards and the volume tenant
// hosts: the volume hosts' shards are paused alongside the node's own, and a
// still-sealed tenant's shards are closed rather than resumed. Both start
// their shards through beginStart from then on. Either may be nil.
func (p *ShardPauser) SetTenants(shards *RaftBackendFactory, hosts *TenantHostRegistry) {
	if p == nil {
		return
	}
	t := &pauserTenants{}
	if shards != nil {
		shards.startGate.Store(p)
		t.shards = shards
	}
	if hosts != nil {
		hosts.startGate.Store(p)
		t.hosts = hosts
	}
	p.tenants.Store(t)
}

// PauseShards pauses every shard of the node's host and of each volume
// tenant's host, all at once, and returns once each pause has: none of
// their log, apply or snapshot work is in flight. A shard that stopped
// meanwhile has nothing to pause. If ctx ends first it returns ctx's error
// and the shards stay paused. No shard starts from the pause until the
// resume (beginStart). See service.ShardPauser.
func (p *ShardPauser) PauseShards(ctx context.Context) error {
	if p == nil {
		return nil
	}
	// From here no shard starts, and every start already admitted has its
	// shard on a host the listing below reads.
	p.setSealed(true)
	p.mu.Lock()
	defer p.mu.Unlock()
	targets := p.targets()
	errs := make(chan error, len(targets))
	var wg sync.WaitGroup
	for key, tenantID := range targets {
		p.paused[key] = tenantID
		wg.Go(func() {
			if err := key.host.PauseShard(ctx, key.shardID); err != nil && !shardGone(err) {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	var all []error
	for err := range errs {
		all = append(all, err)
	}
	return errors.Join(all...)
}

// targets lists every shard of the node's host and of each volume tenant's
// host, each mapped to the volume tenant owning the host.
func (p *ShardPauser) targets() map[pauseKey]string {
	out := make(map[pauseKey]string)
	add := func(host shardHost, tenantID string) {
		info := host.GetNodeHostInfo(quicraft.NodeHostInfoOption{SkipLogInfo: true})
		if info == nil {
			return
		}
		for _, s := range info.ShardInfoList {
			out[pauseKey{host: host, shardID: s.ShardID}] = tenantID
		}
	}
	add(p.host, "")
	if t := p.tenants.Load(); t != nil && t.hosts != nil {
		for tenantID, host := range t.hosts.shardHosts() {
			add(host, tenantID)
		}
	}
	return out
}

// resume runs once the node barrier is unsealed: it closes a still-sealed
// tenant's shards and host and resumes every other shard PauseShards paused,
// then lets shards start again. It runs inside the unseal, so it outlives a
// caller that stops waiting.
func (p *ShardPauser) resume(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.setSealed(false)
	if len(p.paused) == 0 {
		return
	}
	p.closeSealedTenants()
	for key := range p.paused {
		if err := key.host.ResumeShard(ctx, key.shardID); err != nil && !shardGone(err) {
			p.logger.Error("seal: resuming a paused shard failed",
				slog.Uint64("shard", key.shardID), slog.Any("error", err))
		}
	}
	clear(p.paused)
}

// beginStart admits a shard start, holding a pause off until done is called.
// Once a pause ran it refuses with seal.ErrSealed until the resume: the pause
// lists the shards it pauses once, so a shard started after it would serve
// unpaused while the node is sealed. A nil *ShardPauser admits every start.
func (p *ShardPauser) beginStart() (done func(), err error) {
	if p == nil {
		return func() {}, nil
	}
	p.gate.RLock()
	if p.sealed {
		p.gate.RUnlock()
		return nil, seal.ErrSealed
	}
	return p.gate.RUnlock, nil
}

// setSealed flips sealed once every admitted start has finished.
func (p *ShardPauser) setSealed(sealed bool) {
	p.gate.Lock()
	p.sealed = sealed
	p.gate.Unlock()
}

// closeSealedTenants closes the shards and host of every tenant whose barrier
// the node unseal left sealed, as SealEnforcer.SealTenant does, and drops the
// closed hosts' shards from the resume.
func (p *ShardPauser) closeSealedTenants() {
	t := p.tenants.Load()
	if t == nil {
		return
	}
	if t.shards != nil {
		for _, s := range t.shards.ListOnHost() {
			if !p.tenantSealed(s.TenantID) {
				continue
			}
			if err := t.shards.Close(s.TenantID, s.Database); err != nil {
				p.logger.Error("seal: closing a sealed tenant's shard failed",
					slog.String("tenant", s.TenantID), slog.String("database", s.Database), slog.Any("error", err))
			}
		}
	}
	if t.hosts == nil {
		return
	}
	closed := make(map[string]bool)
	for key, tenantID := range p.paused {
		if tenantID == "" || !p.tenantSealed(tenantID) {
			continue
		}
		delete(p.paused, key)
		if closed[tenantID] {
			continue
		}
		closed[tenantID] = true
		if err := t.hosts.Close(tenantID); err != nil {
			p.logger.Error("seal: closing a sealed tenant's host failed",
				slog.String("tenant", tenantID), slog.Any("error", err))
		}
	}
}

// tenantSealed reports whether tenantID has a registered barrier that is
// sealed. A tenant without one has nothing sealed to wait for.
func (p *ShardPauser) tenantSealed(tenantID string) bool {
	tb, err := p.barriers.GetTenant(tenantID)
	return err == nil && tb.IsSealed()
}

// shardGone reports whether err says the shard or its host stopped, leaving
// nothing to pause or resume.
func shardGone(err error) bool {
	return errors.Is(err, quicraft.ErrShardNotFound) || errors.Is(err, quicraft.ErrClosed)
}
