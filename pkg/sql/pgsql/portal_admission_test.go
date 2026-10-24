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
	"errors"
	"maps"
	"slices"
	"strconv"
	"testing"

	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"
	"github.com/jeroenrinzema/psql-wire/pkg/buffer"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// errPortalNotBound is countingPortals' refusal of an Execute on a portal it
// does not hold, as psql-wire's cache refuses one with InvalidCursorName.
var errPortalNotBound = errors.New("countingPortals: portal not bound")

// countingPortals is a wire.PortalCache that holds the portals bound to it,
// as psql-wire's does, and counts the Executes it runs. Once fail is set its
// Bind, Delete and DeleteByStatement refuse with it and change nothing.
// onExecute, when set, runs inside each Execute it runs, as a statement's
// handler runs while psql-wire's cache executes its portal.
type countingPortals struct {
	executes  int
	bound     map[string]*wire.Statement
	fail      error
	onExecute func()
}

var _ wire.PortalCache = (*countingPortals)(nil)

func (c *countingPortals) Bind(_ context.Context, name string, statement *wire.Statement, _ []wire.Parameter, _ []wire.FormatCode) error {
	if c.fail != nil {
		return c.fail
	}
	if c.bound == nil {
		c.bound = map[string]*wire.Statement{}
	}
	c.bound[name] = statement
	return nil
}

func (c *countingPortals) Get(_ context.Context, name string) (*wire.Portal, error) {
	if _, ok := c.bound[name]; ok {
		return &wire.Portal{}, nil
	}
	return nil, nil
}

func (c *countingPortals) Execute(_ context.Context, name string, _ wire.Limit, _ *buffer.Reader, _ *buffer.Writer) error {
	if _, ok := c.bound[name]; !ok {
		return errPortalNotBound
	}
	c.executes++
	if c.onExecute != nil {
		c.onExecute()
	}
	return nil
}

func (c *countingPortals) Delete(_ context.Context, name string) error {
	if c.fail != nil {
		return c.fail
	}
	delete(c.bound, name)
	return nil
}

func (c *countingPortals) DeleteByStatement(_ context.Context, statement *wire.Statement) error {
	if c.fail != nil {
		return c.fail
	}
	for name, bound := range c.bound {
		if bound == statement {
			delete(c.bound, name)
		}
	}
	return nil
}

func (c *countingPortals) Close() { clear(c.bound) }

// newCountingAdmittingPortals returns an admitting portal cache over a
// counting one and a connection context whose session re-admits its identity
// through the returned admitter, as Server builds it.
func newCountingAdmittingPortals(t *testing.T) (*admittingPortals, *countingPortals, context.Context, *suspendableAdmitter) {
	t.Helper()
	inner := &countingPortals{}
	adm := &suspendableAdmitter{}
	ctx, err := newConnectionSessionHandler(provider.WithSessionAdmitter(adm))(context.Background())
	require.NoError(t, err)
	return &admittingPortals{PortalCache: inner}, inner, ctx, adm
}

// trackedPortals returns the names of the portals p tracks, sorted.
func trackedPortals(p *admittingPortals) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Sorted(maps.Keys(p.bound))
}

// A portal's resume is refused with the admitter's *TenantNotActiveError once
// its tenant is suspended, and never reaches psql-wire's cache, which would
// deliver its remaining rows; the portal's first Execute is admitted where
// its statement runs, not here.
func TestAdmittingPortals_ResumeRefusedOnceTenantSuspended(t *testing.T) {
	p, inner, ctx, adm := newCountingAdmittingPortals(t)
	require.NoError(t, p.Bind(ctx, "p", nil, nil, nil))

	adm.suspended.Store(true)
	require.NoError(t, p.Execute(ctx, "p", 1, nil, nil), "a first Execute runs its statement, which admits it")
	requireTenantNotActive(t, p.Execute(ctx, "p", 1, nil, nil))
	requireTenantNotActive(t, p.Execute(ctx, "p", 1, nil, nil))
	require.Equal(t, 1, inner.executes, "a refused resume reached the portal")

	// Bound again, the portal starts afresh.
	require.NoError(t, p.Bind(ctx, "p", nil, nil, nil))
	require.NoError(t, p.Execute(ctx, "p", 1, nil, nil))
	require.Equal(t, 2, inner.executes)
}

// An active tenant's portal resumes, and each portal's resumes are its own:
// executing one portal does not make another's first Execute a resume.
func TestAdmittingPortals_ResumeAdmittedWhileTenantActive(t *testing.T) {
	p, inner, ctx, adm := newCountingAdmittingPortals(t)
	require.NoError(t, p.Bind(ctx, "p", nil, nil, nil))
	require.NoError(t, p.Bind(ctx, "q", nil, nil, nil))
	for range 3 {
		require.NoError(t, p.Execute(ctx, "p", 1, nil, nil))
	}
	require.Equal(t, 3, inner.executes)

	adm.suspended.Store(true)
	require.NoError(t, p.Execute(ctx, "q", 1, nil, nil), "another portal's first Execute")
	requireTenantNotActive(t, p.Execute(ctx, "p", 1, nil, nil))
	require.Equal(t, 4, inner.executes)
}

// An Execute of a portal the inner cache does not hold records nothing: a
// client naming a thousand nonexistent portals leaves no state behind, and
// each Execute gets psql-wire's own refusal.
func TestAdmittingPortals_NonexistentPortalsLeaveNoState(t *testing.T) {
	p, ok := newAdmittingPortals().(*admittingPortals)
	require.True(t, ok)
	ctx := context.Background()
	for i := range 1000 {
		err := p.Execute(ctx, "absent-"+strconv.Itoa(i), 1, nil, nil)
		require.Error(t, err)
		require.Equal(t, codes.InvalidCursorName, psqlerr.GetCode(err))
	}
	require.Empty(t, trackedPortals(p))
}

// The same through the counting double, where the refusal is the inner
// cache's and no Execute reaches a portal.
func TestAdmittingPortals_NonexistentPortalRefusedByTheInnerCache(t *testing.T) {
	p, inner, ctx, adm := newCountingAdmittingPortals(t)
	adm.suspended.Store(true)
	for range 2 {
		require.ErrorIs(t, p.Execute(ctx, "absent", 1, nil, nil), errPortalNotBound)
	}
	require.Empty(t, trackedPortals(p))
	require.Zero(t, inner.executes)
}

// Closing a portal removes it: bound again under the same name after a
// resume, its next Execute is a first Execute.
func TestAdmittingPortals_DeleteRemovesThePortal(t *testing.T) {
	p, inner, ctx, adm := newCountingAdmittingPortals(t)
	require.NoError(t, p.Bind(ctx, "p", nil, nil, nil))
	require.NoError(t, p.Bind(ctx, "q", nil, nil, nil))
	require.NoError(t, p.Execute(ctx, "p", 1, nil, nil))

	require.NoError(t, p.Delete(ctx, "p"))
	require.Equal(t, []string{"q"}, trackedPortals(p))
	require.NoError(t, p.Delete(ctx, "absent"), "closing a portal never bound")

	adm.suspended.Store(true)
	require.ErrorIs(t, p.Execute(ctx, "p", 1, nil, nil), errPortalNotBound)
	require.NoError(t, p.Bind(ctx, "p", nil, nil, nil))
	require.NoError(t, p.Execute(ctx, "p", 1, nil, nil))
	require.Equal(t, 2, inner.executes)
}

// Closing a statement -- or a Parse replacing it -- removes every portal
// bound to it, and only those.
func TestAdmittingPortals_DeleteByStatementRemovesItsPortals(t *testing.T) {
	p, _, ctx, adm := newCountingAdmittingPortals(t)
	closed, kept := &wire.Statement{}, &wire.Statement{}
	for _, b := range []struct {
		name      string
		statement *wire.Statement
	}{{"a", closed}, {"b", closed}, {"c", kept}} {
		require.NoError(t, p.Bind(ctx, b.name, b.statement, nil, nil))
		require.NoError(t, p.Execute(ctx, b.name, 1, nil, nil))
	}

	require.NoError(t, p.DeleteByStatement(ctx, closed))
	require.Equal(t, []string{"c"}, trackedPortals(p))

	adm.suspended.Store(true)
	requireTenantNotActive(t, p.Execute(ctx, "c", 1, nil, nil))
	require.NoError(t, p.Bind(ctx, "a", kept, nil, nil))
	require.NoError(t, p.Execute(ctx, "a", 1, nil, nil), "a portal bound after its statement closed starts afresh")
}

// The connection's end removes every portal.
func TestAdmittingPortals_CloseRemovesEveryPortal(t *testing.T) {
	p, inner, ctx, _ := newCountingAdmittingPortals(t)
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, p.Bind(ctx, name, &wire.Statement{}, nil, nil))
		require.NoError(t, p.Execute(ctx, name, 1, nil, nil))
	}

	p.Close()
	require.Empty(t, trackedPortals(p))
	require.Empty(t, inner.bound)
}

// A Bind replaces the portal it names: the replacement is tracked under its
// own statement and has not been executed.
func TestAdmittingPortals_BindReplacesThePortal(t *testing.T) {
	p, _, ctx, adm := newCountingAdmittingPortals(t)
	first, second := &wire.Statement{}, &wire.Statement{}
	require.NoError(t, p.Bind(ctx, "p", first, nil, nil))
	require.NoError(t, p.Execute(ctx, "p", 1, nil, nil))
	require.NoError(t, p.Bind(ctx, "p", second, nil, nil))

	require.NoError(t, p.DeleteByStatement(ctx, first))
	require.Equal(t, []string{"p"}, trackedPortals(p), "the replaced statement's close removed its replacement")

	adm.suspended.Store(true)
	require.NoError(t, p.Execute(ctx, "p", 1, nil, nil), "the replacement's first Execute")
	require.NoError(t, p.DeleteByStatement(ctx, second))
	require.Empty(t, trackedPortals(p))
}

// A removal the inner cache refuses leaves the portal tracked, so a portal
// the inner cache may still hold keeps having its resumes admitted.
func TestAdmittingPortals_InnerRefusalKeepsThePortalTracked(t *testing.T) {
	p, inner, ctx, adm := newCountingAdmittingPortals(t)
	statement := &wire.Statement{}
	require.NoError(t, p.Bind(ctx, "p", statement, nil, nil))
	require.NoError(t, p.Execute(ctx, "p", 1, nil, nil))

	refusal := errors.New("inner cache refused")
	inner.fail = refusal
	require.ErrorIs(t, p.Bind(ctx, "p", &wire.Statement{}, nil, nil), refusal)
	require.ErrorIs(t, p.Delete(ctx, "p"), refusal)
	require.ErrorIs(t, p.DeleteByStatement(ctx, statement), refusal)
	require.ErrorIs(t, p.Bind(ctx, "q", nil, nil, nil), refusal)
	require.Equal(t, []string{"p"}, trackedPortals(p))

	adm.suspended.Store(true)
	requireTenantNotActive(t, p.Execute(ctx, "p", 1, nil, nil))
}

// The server's portal cache is psql-wire's own, admitting each resume.
// A statement's handler that calls back into the cache while its portal
// executes -- a DEALLOCATE closing the statement, or a portal Close -- runs to
// completion: the cache holds no lock of its own across the inner Execute.
func TestAdmittingPortals_HandlerCallsBackIntoTheCache(t *testing.T) {
	p, inner, ctx, adm := newCountingAdmittingPortals(t)
	statement := &wire.Statement{}
	require.NoError(t, p.Bind(ctx, "p", statement, nil, nil))
	require.NoError(t, p.Bind(ctx, "q", nil, nil, nil))
	require.NoError(t, p.Bind(ctx, "r", &wire.Statement{}, nil, nil))

	inner.onExecute = func() {
		require.NoError(t, p.DeleteByStatement(ctx, statement))
		require.NoError(t, p.Delete(ctx, "q"))
		require.NoError(t, p.Bind(ctx, "s", nil, nil, nil))
	}
	require.NoError(t, p.Execute(ctx, "p", 1, nil, nil))
	inner.onExecute = nil
	require.Equal(t, []string{"r", "s"}, trackedPortals(p))

	adm.suspended.Store(true)
	require.ErrorIs(t, p.Execute(ctx, "p", 1, nil, nil), errPortalNotBound, "the portal its own handler closed")
	require.NoError(t, p.Execute(ctx, "s", 1, nil, nil), "the portal its handler bound")
	requireTenantNotActive(t, p.Execute(ctx, "s", 1, nil, nil))
}

func TestNewAdmittingPortals_WrapsTheDefaultCache(t *testing.T) {
	p, ok := newAdmittingPortals().(*admittingPortals)
	require.True(t, ok)
	require.IsType(t, wire.DefaultPortalCacheFn(), p.PortalCache)
	require.NoError(t, p.Delete(context.Background(), "absent"), "deleting a portal never bound")
}
