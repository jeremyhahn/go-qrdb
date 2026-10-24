// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"maps"
	"sync"

	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/jeroenrinzema/psql-wire/pkg/buffer"
)

// admittingPortals is a connection's psql-wire portal cache that re-admits the
// connection's identity (admitConnection) before it resumes a portal.
//
// An Execute with a row limit suspends its portal (PortalSuspended), and a
// later Execute resumes the portal's row iterator inside psql-wire: it runs no
// statement callback, no HandleParse and no CommandBegin, so none of the
// admissions those apply sees it. Without this check a portal opened while
// its tenant was active would keep serving rows after the tenant's
// suspension, until the seal guard's next admission tick closed the
// connection. A portal's first Execute is admitted where its statement runs;
// only the Executes after it ask here.
//
// Its state is bounded by the inner cache's: it tracks a portal from the Bind
// that puts it in the inner cache to the Delete, DeleteByStatement or Close
// that takes it out, and an Execute of a portal it does not track -- one the
// inner cache refuses as nonexistent -- records nothing. psql-wire removes a
// portal on no other path: its Sync and transaction end leave portals bound.
type admittingPortals struct {
	wire.PortalCache

	// mu guards bound. It is one connection's, so uncontended, and is never
	// held across a call into the inner cache: a statement's handler may call
	// back into the cache (a DEALLOCATE) while its portal executes.
	mu sync.Mutex

	// bound holds each portal the inner cache holds, by name.
	bound map[string]*boundPortal
}

// boundPortal is a bound portal's tracked state.
type boundPortal struct {
	// statement is the prepared statement the portal was bound to, which
	// DeleteByStatement matches on.
	statement *wire.Statement

	// executed is set by the portal's first Execute: every Execute after it
	// resumes the portal.
	executed bool
}

// newAdmittingPortals returns psql-wire's default portal cache, admitting
// each portal resume. It is the server's wire.Portals constructor: one cache
// per connection.
func newAdmittingPortals() wire.PortalCache {
	return &admittingPortals{PortalCache: wire.DefaultPortalCacheFn()}
}

// Bind implements wire.PortalCache. A portal bound again replaces the one it
// names and starts afresh: its next Execute runs its statement, which admits
// it. A Bind the inner cache refuses leaves the tracked portal as it was.
func (p *admittingPortals) Bind(ctx context.Context, name string, statement *wire.Statement, parameters []wire.Parameter, columns []wire.FormatCode) error {
	if err := p.PortalCache.Bind(ctx, name, statement, parameters, columns); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bound == nil {
		p.bound = map[string]*boundPortal{}
	}
	p.bound[name] = &boundPortal{statement: statement}
	return nil
}

// Delete implements wire.PortalCache: the portal Close message.
func (p *admittingPortals) Delete(ctx context.Context, name string) error {
	if err := p.PortalCache.Delete(ctx, name); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.bound, name)
	return nil
}

// DeleteByStatement implements wire.PortalCache: the statement Close message,
// and a Parse that replaces a named statement, remove every portal bound to
// the statement.
func (p *admittingPortals) DeleteByStatement(ctx context.Context, statement *wire.Statement) error {
	if err := p.PortalCache.DeleteByStatement(ctx, statement); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	maps.DeleteFunc(p.bound, func(_ string, bp *boundPortal) bool {
		return bp.statement == statement
	})
	return nil
}

// Close implements wire.PortalCache: the connection's end removes every
// portal.
func (p *admittingPortals) Close() {
	p.PortalCache.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.bound)
}

// Execute implements wire.PortalCache. It refuses to resume a portal whose
// connection's identity is no longer admitted, with the admitter's refusal --
// *authmw.TenantNotActiveError once its tenant is suspended or disabled --
// and delivers none of the portal's remaining rows.
func (p *admittingPortals) Execute(ctx context.Context, name string, limit wire.Limit, reader *buffer.Reader, writer *buffer.Writer) error {
	if p.resumes(name) {
		if err := admitConnection(ctx); err != nil {
			return err
		}
	}
	return p.PortalCache.Execute(ctx, name, limit, reader, writer)
}

// resumes marks the named portal executed and reports whether an earlier
// Execute already had: an Execute of it is a resume. It is false for a
// portal it does not track, and records nothing for one.
func (p *admittingPortals) resumes(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	bp, ok := p.bound[name]
	if !ok {
		return false
	}
	resumed := bp.executed
	bp.executed = true
	return resumed
}
