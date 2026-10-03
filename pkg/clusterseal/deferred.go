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

package clusterseal

import (
	"context"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// Deferred is the Sealer a composer hands to its services before the Monitor
// can exist: the monitor reads the system shard and seals through the shard
// factories, which are built after the services. Until Set, Deferred is
// inactive -- seals are node-local and nothing is refused, exactly as in a
// cluster that has not activated cluster-seal. Anything that must honour the
// records -- the tenant shard reconciler -- therefore runs only after Set.
// The stream registry exists from the start, so a stream opened before Set is
// ended by the monitor.
type Deferred struct {
	streams *Streams
	m       atomic.Pointer[Monitor]
}

var _ Sealer = (*Deferred)(nil)

// NewDeferred returns an unset Deferred with its own stream registry. Build
// the Monitor with Config.Streams set to Streams().
func NewDeferred() *Deferred {
	return &Deferred{streams: NewStreams()}
}

// Set installs the monitor every method forwards to from now on.
func (d *Deferred) Set(m *Monitor) { d.m.Store(m) }

// Streams returns the shared stream registry.
func (d *Deferred) Streams() *Streams { return d.streams }

// Active reports whether seals are cluster-wide.
func (d *Deferred) Active() bool {
	m := d.m.Load()
	return m != nil && m.Active()
}

// Seal forwards to the monitor; before Set it returns ErrInactive.
func (d *Deferred) Seal(ctx context.Context, b Barrier, by Principal) (Record, error) {
	m := d.m.Load()
	if m == nil {
		return Record{}, ErrInactive
	}
	return m.Seal(ctx, b, by)
}

// Clear forwards to the monitor; before Set it returns ErrInactive.
func (d *Deferred) Clear(ctx context.Context, b Barrier, by Principal) (Record, error) {
	m := d.m.Load()
	if m == nil {
		return Record{}, ErrInactive
	}
	return m.Clear(ctx, b, by)
}

// Status forwards to the monitor; before Set it is the inactive Status.
func (d *Deferred) Status(b Barrier) Status {
	m := d.m.Load()
	if m == nil {
		return Status{}
	}
	return m.Status(b)
}

// Refuses forwards to the monitor; before Set nothing is refused.
func (d *Deferred) Refuses(b Barrier) bool {
	m := d.m.Load()
	return m != nil && m.Refuses(b)
}

// Refusing forwards to the monitor; before Set nothing is refused.
func (d *Deferred) Refusing() bool {
	m := d.m.Load()
	return m != nil && m.Refusing()
}

// IsSealed is Refusing, for use as a sealed-window probe term.
func (d *Deferred) IsSealed() bool { return d.Refusing() }

// PrincipalFrom names the authenticated caller in ctx for a seal record, or
// the zero Principal when ctx carries no identity.
func PrincipalFrom(ctx context.Context) Principal {
	id := auth.IdentityFromContext(ctx)
	if id == nil {
		return Principal{}
	}
	return Principal{Name: id.Subject, Tenant: id.TenantID}
}

// StreamsOf returns the stream registry sealer ends streams through, or nil
// when it has none. A node-local seal -- before cluster sealing is active --
// ends its barrier's streams through it, as the monitor ends them for a
// cluster seal.
func StreamsOf(sealer Sealer) *Streams {
	if withStreams, ok := sealer.(interface{ Streams() *Streams }); ok {
		return withStreams.Streams()
	}
	return nil
}
