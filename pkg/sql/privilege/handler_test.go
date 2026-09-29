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

package privilege

import (
	"context"
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	ast "github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

const (
	testDatabase = "shop"
	testTenant   = "acme"
	testSubject  = "alice"
	testGrantee  = "alice@%"
)

// testFixture is one handler over one in-memory privilege catalog, plus the
// session context statements are checked on.
type testFixture struct {
	handler *handler
	harness *sqltest.Harness
	ctx     *sqle.Context
}

// newFixture builds a handler over a real (empty, therefore unlatched)
// privilege catalog, with a session for the given tenant and subject.
func newFixture(t *testing.T, tenantID, subject string) *testFixture {
	t.Helper()

	h := sqltest.NewHarness()
	sess := newFakeSession(tenantID, subject)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	ctx.SetCurrentDatabase(testDatabase)

	return &testFixture{
		handler: &handler{registry: h.Registry},
		harness: h,
		ctx:     ctx,
	}
}

// grant writes a grant for the fixture's grantee, arming the catalog.
func (f *testFixture) grant(t *testing.T, grants ...service.PrivilegeGrant) {
	t.Helper()
	require.NoError(t, f.harness.PrivSvc.Grant(context.Background(), &service.GrantPrivilegesRequest{
		Database: testDatabase, Grantee: testGrantee, Grants: grants,
	}))
}

// state resolves a fresh query state, which is what go-mysql-server does once
// per statement.
func (f *testFixture) state() sqle.AuthorizationQueryState {
	return f.handler.NewQueryState(f.ctx)
}

// info builds one AuthInformation.
func info(authType, targetType string, names ...string) ast.AuthInformation {
	return ast.AuthInformation{AuthType: authType, TargetType: targetType, TargetNames: names}
}

// ---------------------------------------------------------------------------
// Exemption
// ---------------------------------------------------------------------------

func TestHandler_NoRegistryPermitsEverything(t *testing.T) {
	h := &handler{}
	sess := newFakeSession(testTenant, testSubject)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	ctx.SetCurrentDatabase(testDatabase)

	state := h.NewQueryState(ctx)
	require.NoError(t, state.Error())
	assert.NoError(t, h.HandleAuth(ctx, state, info(ast.AuthType_SELECT,
		ast.AuthTargetType_SingleTableIdentifier, "", "orders")))
	assert.NoError(t, h.CheckDatabase(ctx, state, testDatabase))
	assert.NoError(t, h.CheckTable(ctx, state, testDatabase, "", "orders"))
	assert.NoError(t, h.HandleAuthNode(ctx, state, nil))
}

func TestHandler_SystemTenantIsExempt(t *testing.T) {
	f := newFixture(t, tenantid.System, "operator")
	f.grant(t, service.PrivilegeGrant{Table: "unrelated", Privileges: service.PrivilegeSelect})

	state := f.state()
	require.NoError(t, state.Error())
	assert.NoError(t, f.handler.HandleAuth(f.ctx, state, info(ast.AuthType_SELECT,
		ast.AuthTargetType_SingleTableIdentifier, "", "orders")))
}

func TestHandler_UnlatchedDatabasePermitsEverything(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)

	state := f.state()
	require.NoError(t, state.Error())
	for _, auth := range []ast.AuthInformation{
		info(ast.AuthType_SELECT, ast.AuthTargetType_SingleTableIdentifier, "", "orders"),
		info(ast.AuthType_CREATE, ast.AuthTargetType_DatabaseIdentifiers),
		info(ast.AuthType_DROP, ast.AuthTargetType_MultipleTableIdentifiers, "", "orders"),
		info(ast.AuthType_RENAME, ast.AuthTargetType_Ignore, "", "orders", "", "people"),
	} {
		assert.NoErrorf(t, f.handler.HandleAuth(f.ctx, state, auth),
			"unlatched database refused %s/%s", auth.AuthType, auth.TargetType)
	}
}

func TestHandler_SessionWithoutCurrentDatabaseResolvesNothing(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.ctx.SetCurrentDatabase("")

	state := f.handler.NewQueryState(f.ctx).(*queryState)
	require.NoError(t, state.Error())
	assert.Nil(t, state.resolved)
	assert.NoError(t, f.handler.HandleAuth(f.ctx, state, info(ast.AuthType_SELECT, ast.AuthTargetType_Ignore)))
}

func TestHandler_ReservedSchemaIsNeverChecked(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.grant(t, service.PrivilegeGrant{Table: "unrelated", Privileges: service.PrivilegeSelect})

	state := f.state()
	for _, schema := range []string{sqle.InformationSchemaDatabaseName, mySQLSchemaName} {
		assert.NoError(t, f.handler.HandleAuth(f.ctx, state,
			info(ast.AuthType_SELECT, ast.AuthTargetType_SingleTableIdentifier, schema, "anything")))
		assert.NoError(t, f.handler.CheckDatabase(f.ctx, state, schema))
		assert.NoError(t, f.handler.CheckTable(f.ctx, state, schema, "", "anything"))
	}
}

// ---------------------------------------------------------------------------
// Statement dispatch
// ---------------------------------------------------------------------------

// TestHandler_AuthTypeRequirements drives every modelled authorization type
// through the handler twice: once with the privilege it names and once
// without.
func TestHandler_AuthTypeRequirements(t *testing.T) {
	tests := []struct {
		authType string
		needs    service.PrivilegeMask
	}{
		{ast.AuthType_SELECT, service.PrivilegeSelect},
		{ast.AuthType_INSERT, service.PrivilegeInsert},
		{ast.AuthType_UPDATE, service.PrivilegeUpdate},
		{ast.AuthType_DELETE, service.PrivilegeDelete},
		{ast.AuthType_REPLACE, service.PrivilegeInsert | service.PrivilegeDelete},
		{ast.AuthType_CREATE, service.PrivilegeCreate},
		{ast.AuthType_DROP, service.PrivilegeDrop},
		{ast.AuthType_ALTER, service.PrivilegeAlter},
		{ast.AuthType_INDEX, service.PrivilegeIndex},
		{ast.AuthType_FOREIGN_KEY, service.PrivilegeReferences},
		{ast.AuthType_GRANT_PRIVILEGE, service.PrivilegeGrantOption},
		{ast.AuthType_REVOKE_PRIVILEGE, service.PrivilegeGrantOption},
		{ast.AuthType_REVOKE_ALL, service.PrivilegeGrantOption},
	}

	for _, tc := range tests {
		t.Run(tc.authType, func(t *testing.T) {
			auth := info(tc.authType, ast.AuthTargetType_SingleTableIdentifier, "", "orders")

			denied := newFixture(t, testTenant, testSubject)
			denied.grant(t, service.PrivilegeGrant{Table: "orders", Privileges: 0})
			assertDenied(t, denied.handler.HandleAuth(denied.ctx, denied.state(), auth))

			permitted := newFixture(t, testTenant, testSubject)
			permitted.grant(t, service.PrivilegeGrant{Table: "orders", Privileges: tc.needs})
			assert.NoError(t, permitted.handler.HandleAuth(permitted.ctx, permitted.state(), auth))
		})
	}
}

func TestHandler_AlwaysPermittedAuthTypes(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.grant(t, service.PrivilegeGrant{Table: "orders", Privileges: 0})

	state := f.state()
	for _, authType := range []string{ast.AuthType_IGNORE, ast.AuthType_SHOW} {
		assert.NoErrorf(t, f.handler.HandleAuth(f.ctx, state, info(authType, "")),
			"%s must be permitted regardless of the catalog", authType)
	}
}

func TestHandler_UnmodelledAuthTypeFailsClosed(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.grant(t, service.PrivilegeGrant{Privileges: service.PrivilegeAll | service.PrivilegeGrantOption})

	state := f.state()
	for _, authType := range []string{
		ast.AuthType_SUPER,
		ast.AuthType_PROCESS,
		ast.AuthType_TRIGGER,
		ast.AuthType_CREATE_USER,
		ast.AuthType_LOCK,
		ast.AuthType_CREATE_TEMP,
		"A_TYPE_INVENTED_BY_A_FUTURE_PARSER",
	} {
		assertDenied(t, f.handler.HandleAuth(f.ctx, state, info(authType, ast.AuthTargetType_Global)),
			"%s must fail closed: no grant in this plane can satisfy it", authType)
	}
}

func TestHandler_UnmodelledTargetTypeFailsClosed(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.grant(t, service.PrivilegeGrant{Privileges: service.PrivilegeAll})

	assertDenied(t, f.handler.HandleAuth(f.ctx, f.state(),
		info(ast.AuthType_SELECT, "A_TARGET_SHAPE_INVENTED_BY_A_FUTURE_PARSER")))
}

// ---------------------------------------------------------------------------
// Target dispatch
// ---------------------------------------------------------------------------

func TestHandler_TargetShapes(t *testing.T) {
	tests := []struct {
		name       string
		auth       ast.AuthInformation
		grant      service.PrivilegeGrant
		wantDenied bool
	}{
		{
			name:  "no target falls back to the session database tier",
			auth:  info(ast.AuthType_SELECT, ast.AuthTargetType_Ignore),
			grant: service.PrivilegeGrant{Privileges: service.PrivilegeSelect},
		},
		{
			name:       "no target is refused without the database tier",
			auth:       info(ast.AuthType_SELECT, ast.AuthTargetType_Ignore),
			grant:      service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeSelect},
			wantDenied: true,
		},
		{
			name:  "a server-level target defers to the control plane",
			auth:  info(ast.AuthType_CREATE, ast.AuthTargetType_Global),
			grant: service.PrivilegeGrant{Privileges: 0},
		},
		{
			name:  "an unclassified target is permitted",
			auth:  info(ast.AuthType_SHOW, ast.AuthTargetType_TODO),
			grant: service.PrivilegeGrant{Privileges: 0},
		},
		{
			name:  "an empty database list means the session database",
			auth:  info(ast.AuthType_CREATE, ast.AuthTargetType_DatabaseIdentifiers),
			grant: service.PrivilegeGrant{Privileges: service.PrivilegeCreate},
		},
		{
			name:       "an empty database list is still checked",
			auth:       info(ast.AuthType_CREATE, ast.AuthTargetType_DatabaseIdentifiers),
			grant:      service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeCreate},
			wantDenied: true,
		},
		{
			name:  "a named database list",
			auth:  info(ast.AuthType_CREATE, ast.AuthTargetType_DatabaseIdentifiers, testDatabase),
			grant: service.PrivilegeGrant{Privileges: service.PrivilegeCreate},
		},
		{
			name:  "several table identifiers",
			auth:  info(ast.AuthType_DROP, ast.AuthTargetType_MultipleTableIdentifiers, "", "orders", "", "customers"),
			grant: service.PrivilegeGrant{Privileges: service.PrivilegeDrop},
		},
		{
			name:       "several table identifiers, one of them ungranted",
			auth:       info(ast.AuthType_DROP, ast.AuthTargetType_MultipleTableIdentifiers, "", "orders", "", "customers"),
			grant:      service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeDrop},
			wantDenied: true,
		},
		{
			name:  "a column identifier",
			auth:  info(ast.AuthType_SELECT, ast.AuthTargetType_TableColumn, "", "orders", "total"),
			grant: service.PrivilegeGrant{Table: "orders", Column: "total", Privileges: service.PrivilegeSelect},
		},
		{
			name:       "a column identifier the grantee does not hold",
			auth:       info(ast.AuthType_SELECT, ast.AuthTargetType_TableColumn, "", "orders", "secret"),
			grant:      service.PrivilegeGrant{Table: "orders", Column: "total", Privileges: service.PrivilegeSelect},
			wantDenied: true,
		},
		{
			name:  "database visibility with a privilege anywhere",
			auth:  info(ast.AuthType_VISIBLE, ast.AuthTargetType_DatabaseIdentifiers, testDatabase),
			grant: service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeSelect},
		},
		{
			name:  "database visibility with an unclassified target",
			auth:  info(ast.AuthType_VISIBLE, ast.AuthTargetType_TODO),
			grant: service.PrivilegeGrant{Privileges: 0},
		},
		{
			name:       "database visibility with nothing held anywhere",
			auth:       info(ast.AuthType_VISIBLE, ast.AuthTargetType_DatabaseIdentifiers, testDatabase),
			grant:      service.PrivilegeGrant{Privileges: 0},
			wantDenied: true,
		},
		{
			name: "rename with both halves of the requirement",
			auth: info(ast.AuthType_RENAME, ast.AuthTargetType_Ignore, "", "orders", "", "archive"),
			grant: service.PrivilegeGrant{Privileges: service.PrivilegeAlter | service.PrivilegeDrop |
				service.PrivilegeCreate | service.PrivilegeInsert},
		},
		{
			name: "rename without the target half",
			auth: info(ast.AuthType_RENAME, ast.AuthTargetType_Ignore, "", "orders", "", "archive"),
			grant: service.PrivilegeGrant{Table: "orders",
				Privileges: service.PrivilegeAlter | service.PrivilegeDrop},
			wantDenied: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, testTenant, testSubject)
			f.grant(t, tc.grant)

			err := f.handler.HandleAuth(f.ctx, f.state(), tc.auth)
			if tc.wantDenied {
				assertDenied(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestHandler_MalformedTargetNamesFailClosed pins that a target list whose
// arity does not match its declared shape is refused rather than indexed into.
func TestHandler_MalformedTargetNamesFailClosed(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.grant(t, service.PrivilegeGrant{Privileges: service.PrivilegeAll})

	state := f.state()
	for _, auth := range []ast.AuthInformation{
		info(ast.AuthType_SELECT, ast.AuthTargetType_SingleTableIdentifier, "onlyone"),
		info(ast.AuthType_DROP, ast.AuthTargetType_MultipleTableIdentifiers, "odd", "count", "here"),
		info(ast.AuthType_SELECT, ast.AuthTargetType_TableColumn, "", "orders"),
		info(ast.AuthType_RENAME, ast.AuthTargetType_Ignore, "", "orders", ""),
	} {
		assertDenied(t, f.handler.HandleAuth(f.ctx, state, auth),
			"%s/%s with %d names must fail closed", auth.AuthType, auth.TargetType, len(auth.TargetNames))
	}
}

// ---------------------------------------------------------------------------
// Reachability checks
// ---------------------------------------------------------------------------

func TestHandler_CheckDatabase(t *testing.T) {
	unlatched := newFixture(t, testTenant, testSubject)
	assert.NoError(t, unlatched.handler.CheckDatabase(unlatched.ctx, unlatched.state(), testDatabase))

	empty := newFixture(t, testTenant, testSubject)
	empty.grant(t, service.PrivilegeGrant{Privileges: 0})
	assertDeniedWithCode(t, ErrCodeDatabaseAccessDenied,
		empty.handler.CheckDatabase(empty.ctx, empty.state(), testDatabase))

	held := newFixture(t, testTenant, testSubject)
	held.grant(t, service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeSelect})
	assert.NoError(t, held.handler.CheckDatabase(held.ctx, held.state(), testDatabase))
	assert.NoError(t, held.handler.CheckSchema(held.ctx, held.state(), testDatabase, "public"))
}

func TestHandler_CheckTable(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.grant(t, service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeSelect})

	state := f.state()
	assert.NoError(t, f.handler.CheckTable(f.ctx, state, testDatabase, "", "orders"))
	assertDeniedWithCode(t, ErrCodeTableAccessDenied,
		f.handler.CheckTable(f.ctx, state, testDatabase, "", "customers"))

	// A grantee holding nothing anywhere is reported at the database level,
	// which is the distinction MySQL draws.
	nothing := newFixture(t, testTenant, testSubject)
	nothing.grant(t, service.PrivilegeGrant{Privileges: 0})
	assertDeniedWithCode(t, ErrCodeDatabaseAccessDenied,
		nothing.handler.CheckTable(nothing.ctx, nothing.state(), testDatabase, "", "orders"))

	// An empty table name asks only about the database.
	assert.NoError(t, f.handler.CheckTable(f.ctx, state, testDatabase, "", ""))
}

func TestHandler_HandleAuthNode(t *testing.T) {
	unlatched := newFixture(t, testTenant, testSubject)
	assert.NoError(t, unlatched.handler.HandleAuthNode(unlatched.ctx, unlatched.state(), nil),
		"an unlatched database has nothing to refuse")

	latched := newFixture(t, testTenant, testSubject)
	latched.grant(t, service.PrivilegeGrant{Privileges: service.PrivilegeAll})
	assertDenied(t, latched.handler.HandleAuthNode(latched.ctx, latched.state(), nil),
		"qrdb registers no table functions, so no grant can authorise one")
}

// ---------------------------------------------------------------------------
// Query state lifecycle
// ---------------------------------------------------------------------------

// TestHandler_NilQueryStateIsRebuilt pins the interface's allowance that the
// state may be nil: every entry point must construct one rather than trust it.
func TestHandler_NilQueryStateIsRebuilt(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.grant(t, service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeSelect})

	assert.NoError(t, f.handler.HandleAuth(f.ctx, nil,
		info(ast.AuthType_SELECT, ast.AuthTargetType_SingleTableIdentifier, "", "orders")))
	assertDenied(t, f.handler.HandleAuth(f.ctx, nil,
		info(ast.AuthType_DELETE, ast.AuthTargetType_SingleTableIdentifier, "", "orders")))
	assert.NoError(t, f.handler.CheckDatabase(f.ctx, nil, testDatabase))
	assert.NoError(t, f.handler.CheckTable(f.ctx, nil, testDatabase, "", "orders"))
	assertDenied(t, f.handler.HandleAuthNode(f.ctx, nil, nil))
}

// foreignState is another handler's query state, which a mixed-engine process
// could hand back.
type foreignState struct{}

func (foreignState) Error() error                 { return nil }
func (foreignState) AuthorizationQueryStateImpl() {}

func TestHandler_ForeignQueryStateIsRebuilt(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.grant(t, service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeSelect})

	assertDenied(t, f.handler.HandleAuth(f.ctx, foreignState{},
		info(ast.AuthType_DELETE, ast.AuthTargetType_SingleTableIdentifier, "", "orders")),
		"a state belonging to another handler must not be trusted")

	// A state belonging to a DIFFERENT instance of this same type must also
	// be rebuilt, since it caches that handler's snapshots, not this one's.
	other := newFixture(t, testTenant, testSubject)
	assertDenied(t, f.handler.HandleAuth(f.ctx, other.state(),
		info(ast.AuthType_DELETE, ast.AuthTargetType_SingleTableIdentifier, "", "orders")))
}

// failingRegistry reports a resolution failure for the privilege catalog.
type failingRegistry struct {
	service.Registry
	err error
}

func (r *failingRegistry) PrivilegeService(string, string) (service.PrivilegeServicer, error) {
	return nil, r.err
}

func (r *failingRegistry) CollectionService(string, string) (service.CollectionServicer, error) {
	return nil, r.err
}

func TestHandler_RegistryFailurePropagatesAndFailsClosed(t *testing.T) {
	sentinel := errors.New("tenant not found")
	h := &handler{registry: &failingRegistry{err: sentinel}}

	sess := newFakeSession(testTenant, testSubject)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	ctx.SetCurrentDatabase(testDatabase)

	state := h.NewQueryState(ctx)
	require.ErrorIs(t, state.Error(), sentinel,
		"go-mysql-server checks Error() before building the plan, so the query must fail")

	// Every entry point must surface the same failure rather than proceed.
	assert.ErrorIs(t, h.HandleAuth(ctx, state, info(ast.AuthType_SELECT, ast.AuthTargetType_Ignore)), sentinel)
	assert.ErrorIs(t, h.CheckDatabase(ctx, state, testDatabase), sentinel)
	assert.ErrorIs(t, h.CheckSchema(ctx, state, testDatabase, ""), sentinel)
	assert.ErrorIs(t, h.CheckTable(ctx, state, testDatabase, "", "orders"), sentinel)
	assert.ErrorIs(t, h.HandleAuthNode(ctx, state, nil), sentinel)
}

// TestHandler_ResolutionFailureForAnotherDatabaseSurfaces pins that a check
// naming a database whose catalog cannot be resolved fails rather than being
// treated as unrestricted.
func TestHandler_ResolutionFailureForAnotherDatabaseSurfaces(t *testing.T) {
	sentinel := errors.New("no such database")
	h := &handler{registry: &conditionalRegistry{
		ok:  sqltest.NewHarness(),
		err: sentinel,
		bad: "elsewhere",
	}}

	sess := newFakeSession(testTenant, testSubject)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	ctx.SetCurrentDatabase(testDatabase)

	state := h.NewQueryState(ctx)
	require.NoError(t, state.Error())
	assert.ErrorIs(t, h.CheckDatabase(ctx, state, "elsewhere"), sentinel)
	assert.ErrorIs(t, h.CheckTable(ctx, state, "elsewhere", "", "orders"), sentinel)
	assert.ErrorIs(t, h.HandleAuth(ctx, state,
		info(ast.AuthType_SELECT, ast.AuthTargetType_SingleTableIdentifier, "elsewhere", "orders")), sentinel)
	assert.ErrorIs(t, h.HandleAuth(ctx, state,
		info(ast.AuthType_SELECT, ast.AuthTargetType_TableColumn, "elsewhere", "orders", "total")), sentinel)
	assert.ErrorIs(t, h.HandleAuth(ctx, state,
		info(ast.AuthType_CREATE, ast.AuthTargetType_DatabaseIdentifiers, "elsewhere")), sentinel)
}

// conditionalRegistry serves one harness for every database except bad, for
// which it reports err.
type conditionalRegistry struct {
	service.Registry
	ok  *sqltest.Harness
	err error
	bad string
}

// CollectionService is implemented explicitly for the reason failingRegistry's
// is: the SQL provider's table lookup reaches it, and a method promoted off the
// embedded nil service.Registry compiles and then dereferences nil. Serving no
// collections is a nil servicer and a nil error, not an error.
func (r *conditionalRegistry) CollectionService(string, string) (service.CollectionServicer, error) {
	return nil, nil
}

func (r *conditionalRegistry) PrivilegeService(_, db string) (service.PrivilegeServicer, error) {
	if db == r.bad {
		return nil, r.err
	}
	return r.ok.PrivSvc, nil
}

// ---------------------------------------------------------------------------
// Snapshot cache
// ---------------------------------------------------------------------------

// TestHandler_SnapshotIsCachedAcrossQueries pins the epoch gate: a second
// query at the same catalog generation reuses the parsed privilege set rather
// than re-reading it.
func TestHandler_SnapshotIsCachedAcrossQueries(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.grant(t, service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeSelect})

	first := f.handler.NewQueryState(f.ctx).(*queryState)
	require.NotNil(t, first.resolved)
	require.NotNil(t, first.resolved.set)

	second := f.handler.NewQueryState(f.ctx).(*queryState)
	require.NotNil(t, second.resolved)
	assert.Same(t, first.resolved.set, second.resolved.set,
		"an unchanged catalog generation must reuse the parsed set, not re-decode it")
	assert.Equal(t, first.resolved.generation, second.resolved.generation)
}

func TestHandler_SnapshotIsInvalidatedByAMutation(t *testing.T) {
	f := newFixture(t, testTenant, testSubject)
	f.grant(t, service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeSelect})

	first := f.handler.NewQueryState(f.ctx).(*queryState)
	f.grant(t, service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeDelete})

	second := f.handler.NewQueryState(f.ctx).(*queryState)
	assert.Greater(t, second.resolved.generation, first.resolved.generation)
	assert.NotSame(t, first.resolved.set, second.resolved.set)
	assert.True(t, second.resolved.set.Table("orders").Has(service.PrivilegeDelete))
}

func TestHandler_SlotIsStablePerKey(t *testing.T) {
	h := &handler{}
	key := cacheKey{tenant: testTenant, database: testDatabase, grantee: testGrantee}

	assert.Same(t, h.slot(key), h.slot(key))
	assert.NotSame(t, h.slot(key), h.slot(cacheKey{tenant: "other", database: testDatabase, grantee: testGrantee}))
}

// ---------------------------------------------------------------------------
// Catalog discovery
// ---------------------------------------------------------------------------

// providerWithoutRegistry is a database provider that does not expose a
// service registry -- the legacy KV-only shape, and any third-party provider.
type providerWithoutRegistry struct {
	sqle.DatabaseProvider
}

// providerWithNilRegistry exposes the accessor but has nothing behind it.
type providerWithNilRegistry struct {
	sqle.DatabaseProvider
}

func (providerWithNilRegistry) Registry() service.Registry { return nil }

// providerWithRegistry exposes a real registry.
type providerWithRegistry struct {
	sqle.DatabaseProvider
	registry service.Registry
}

func (p providerWithRegistry) Registry() service.Registry { return p.registry }

func TestRegistryOf(t *testing.T) {
	assert.Nil(t, registryOf(nil), "a nil catalog yields no registry")

	fake := &fakeCatalog{}
	assert.Nil(t, registryOf(fake), "a catalog that is not go-mysql-server's yields no registry")

	assert.Nil(t, registryOf(analyzer.NewCatalog(providerWithoutRegistry{}, sqle.EngineOverrides{})),
		"a provider with no registry accessor yields no registry")
	assert.Nil(t, registryOf(analyzer.NewCatalog(providerWithNilRegistry{}, sqle.EngineOverrides{})))

	reg := sqltest.NewHarness().Registry
	assert.Equal(t, reg, registryOf(analyzer.NewCatalog(providerWithRegistry{registry: reg}, sqle.EngineOverrides{})))
}

// fakeCatalog is a sql.Catalog that is not *analyzer.Catalog. The embedded
// interface is nil: nothing in this package may call through it.
type fakeCatalog struct{ sqle.Catalog }

func TestCreateHandler(t *testing.T) {
	reg := sqltest.NewHarness().Registry
	created := handlerFactory{}.CreateHandler(analyzer.NewCatalog(providerWithRegistry{registry: reg}, sqle.EngineOverrides{}))

	h, ok := created.(*handler)
	require.True(t, ok)
	assert.Equal(t, reg, h.registry)
}

// TestFactoryIsInstalledGlobally pins that this package's init wins over
// go-mysql-server's own default, which is what makes enforcement reach every
// engine in the process without any per-engine wiring.
func TestFactoryIsInstalledGlobally(t *testing.T) {
	assert.IsType(t, handlerFactory{}, sqle.GetAuthorizationHandlerFactory())
}

// ---------------------------------------------------------------------------
// Assertions
// ---------------------------------------------------------------------------

// assertDenied asserts that err is a MySQL access-denied refusal.
func assertDenied(t *testing.T, err error, msgAndArgs ...any) {
	t.Helper()
	require.Error(t, err, msgAndArgs...)

	sqlErr := sqle.CastSQLError(err)
	require.NotNilf(t, sqlErr, "refusal carried no MySQL error code: %T %v", err, err)
	assert.Contains(t, []int{
		ErrCodeDatabaseAccessDenied, ErrCodeTableAccessDenied, ErrCodeColumnAccessDenied,
	}, sqlErr.Num, msgAndArgs...)
}

// assertDeniedWithCode asserts the exact MySQL error number of a refusal.
func assertDeniedWithCode(t *testing.T, want int, err error) {
	t.Helper()
	require.Error(t, err)

	sqlErr := sqle.CastSQLError(err)
	require.NotNil(t, sqlErr)
	assert.Equal(t, want, sqlErr.Num)
}
