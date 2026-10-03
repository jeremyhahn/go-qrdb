//go:build qrdb_sql

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
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// fakeSession is a real go-mysql-server session carrying a qrdb identity: it
// implements the narrow identitySession slice this package reads, over a
// genuine *sql.BaseSession so it can also be handed to the engine as an
// ordinary session.
type fakeSession struct {
	*sqle.BaseSession

	identity *auth.Identity
	tenantID string
}

// newFakeSession builds a session for the given tenant and subject. An empty
// subject means the session carries no named identity at all.
func newFakeSession(tenantID, subject string) *fakeSession {
	s := &fakeSession{BaseSession: sqle.NewBaseSession(), tenantID: tenantID}
	if subject != "" {
		s.identity = &auth.Identity{Subject: subject, TenantID: tenantID}
	}
	return s
}

func (s *fakeSession) Identity() *auth.Identity { return s.identity }
func (s *fakeSession) TenantID() string         { return s.tenantID }

// plainSession is a session that is not a qrdb session at all.
type plainSession struct{ *sqle.BaseSession }

func TestPrincipalOf(t *testing.T) {
	tests := []struct {
		name        string
		session     sqle.Session
		wantTenant  string
		wantGrantee string
		wantExempt  bool
	}{
		{
			name:       "a session that is not a qrdb session is exempt",
			session:    &plainSession{BaseSession: sqle.NewBaseSession()},
			wantTenant: tenantid.System,
			wantExempt: true,
		},
		{
			name:        "the system tenant is exempt permanently but still named",
			session:     newFakeSession(tenantid.System, "operator"),
			wantTenant:  tenantid.System,
			wantGrantee: "operator@%",
			wantExempt:  true,
		},
		{
			name:        "an empty tenant resolves to the system tenant",
			session:     newFakeSession("", "operator"),
			wantTenant:  tenantid.System,
			wantGrantee: "operator@%",
			wantExempt:  true,
		},
		{
			name:       "a tenant session with no identity is exempt",
			session:    newFakeSession("acme", ""),
			wantTenant: "acme",
			wantExempt: true,
		},
		{
			name:       "a tenant session with an empty subject is exempt",
			session:    &fakeSession{BaseSession: sqle.NewBaseSession(), tenantID: "acme", identity: &auth.Identity{}},
			wantTenant: "acme",
			wantExempt: true,
		},
		{
			name:        "a named tenant identity is governed",
			session:     newFakeSession("acme", "alice"),
			wantTenant:  "acme",
			wantGrantee: "alice@%",
		},
		{
			name:        "a subject already carrying the any-host suffix round-trips",
			session:     newFakeSession("acme", "alice@%"),
			wantTenant:  "acme",
			wantGrantee: "alice@%",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := principalOf(tc.session)
			assert.Equal(t, tc.wantTenant, p.tenant)
			assert.Equal(t, tc.wantGrantee, p.grantee)
			assert.Equal(t, tc.wantExempt, p.exempt)
		})
	}
}

func TestCanonicalGranteeAndName(t *testing.T) {
	assert.Equal(t, "alice@%", canonicalGrantee("alice"))
	assert.Equal(t, "alice@%", canonicalGrantee("alice@%"))
	assert.Equal(t, "alice", granteeName("alice@%"))
	assert.Equal(t, "alice", granteeName("alice"),
		"granteeName must be total, so a value that never carried the suffix survives")
}

func TestIsReservedSchema(t *testing.T) {
	// pg_catalog belongs here for the same structural reason
	// information_schema does: this plane resolves a privilege snapshot per
	// (tenant, database), and a system schema is not a tenant database, so
	// asking the registry for one fails the query outright instead of
	// producing a permission decision.
	for _, name := range []string{
		"information_schema", "INFORMATION_SCHEMA",
		"mysql", "MySQL",
		"pg_catalog", "PG_CATALOG", "Pg_Catalog",
	} {
		assert.True(t, isReservedSchema(name), "%q must be reserved", name)
	}
	for _, name := range []string{"", "shop", "mysqldb", "pg_catalogue", "pgcatalog"} {
		assert.False(t, isReservedSchema(name), "%q must not be reserved", name)
	}
}
