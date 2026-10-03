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
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// anyHost is the only host component a qrdb grantee may carry. qrdb
// identities are authenticated by certificate subject (or by whatever
// authenticator the transport is configured with) and have no network-host
// dimension, so 'user'@'%' is the only form that can ever match a session.
const anyHost = "%"

// identitySession is the narrow slice of pkg/sql/provider.QRDBSession this
// package needs, declared structurally rather than imported.
//
// Structural, not nominal, on purpose: this package is installed into
// go-mysql-server as a process-wide handler and is handed whatever session
// and provider the engine was built with. Depending on the concrete types
// would tie enforcement to one construction path; depending on the two
// methods it actually reads ties it to nothing.
type identitySession interface {
	// Identity returns the authenticated identity, or nil when the session
	// was established without authentication.
	Identity() *auth.Identity

	// TenantID returns the tenant namespace the session executes in,
	// falling back to the system tenant when the identity carries none.
	TenantID() string
}

// principal is the authenticated caller behind one SQL session, reduced to
// what the privilege plane needs.
type principal struct {
	// tenant is the tenant namespace whose privilege catalog applies.
	tenant string

	// grantee is the canonical grantee name ("subject@%"), empty when the
	// session carries no named identity.
	grantee string

	// exempt reports that this session is not subject to privilege checks
	// at all: it belongs to the system tenant, which retains implicit ALL
	// PRIVILEGES on every database permanently.
	//
	// The exemption is what makes the activation latch safe to arm. qrdb
	// has no --skip-grant-tables recovery mode, so if latching could lock
	// out every caller there would be no way back; the system tenant is
	// the guaranteed way back.
	exempt bool
}

// principalOf reduces a session to its principal. A session that is not a
// qrdb session, or that carries no named identity, or that belongs to the
// system tenant, is exempt -- which is exactly the posture every deployment
// running without SQL authentication has always had.
//
// The grantee is resolved independently of the exemption. An exempt session
// still has a name, and still needs one: SHOW GRANTS with no FOR clause
// reports the session's own account whether or not that account is subject to
// enforcement.
func principalOf(sess sqle.Session) principal {
	ident, ok := sess.(identitySession)
	if !ok {
		return principal{tenant: tenantid.System, exempt: true}
	}

	tenantID := ident.TenantID()
	if tenantID == "" {
		tenantID = tenantid.System
	}

	p := principal{tenant: tenantID}
	if id := ident.Identity(); id != nil && id.Subject != "" {
		p.grantee = canonicalGrantee(id.Subject)
	}
	p.exempt = tenantID == tenantid.System || p.grantee == ""
	return p
}

// canonicalGrantee renders a subject as the catalog's grantee key. A subject
// that already carries the any-host suffix is left alone so that a value read
// back out of the catalog round-trips.
func canonicalGrantee(subject string) string {
	if strings.HasSuffix(subject, "@"+anyHost) {
		return subject
	}
	return subject + "@" + anyHost
}

// granteeName splits a canonical grantee back into its user component, for
// rendering in SHOW GRANTS and in error messages.
func granteeName(grantee string) string {
	return strings.TrimSuffix(grantee, "@"+anyHost)
}

// registrySource is the narrow slice of pkg/sql/provider.Provider this
// package needs: the per-(tenant, database) service registry the privilege
// catalog is resolved through. It is asserted against
// analyzer.Catalog.DbProvider, structurally, for the reason given on
// identitySession.
type registrySource interface {
	// Registry returns the service registry, or nil when the provider
	// serves no relational catalog.
	Registry() service.Registry
}
