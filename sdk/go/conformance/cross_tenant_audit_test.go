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

package conformance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"google.golang.org/grpc/codes"

	mcpwire "github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	quicwire "github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Cross-tenant confinement (owner ruling: NO cross-tenant access for anyone).
//
// The audit query took tenant_id from the request unchecked, and an omitted
// one read every tenant's events, so a tenant Admin read the whole node's
// trail. The cells here, and the certificate cells that share their harness,
// boot one listener each and act as the built-in Admin of ONE tenant, over
// that transport, through real mTLS and real RBAC on the remote cells.

const (
	confineAcme   = "acme"
	confineGlobex = "globex"

	// confineSeeder is the actor of every seeded audit event. Queries filter
	// on it, so the authorization decisions the queries themselves emit (which
	// name the caller as actor) never enter the result, and paging terminates.
	confineSeeder = "confinement-seeder"
)

// confinedCell is one transport's clients acting as the Admin of callerTenant.
type confinedCell struct {
	client       *sdk.Client
	node         *node.Node
	ctx          context.Context
	callerTenant string
}

// seededAuditEvents are emitted into the node's audit logger, one per tenant
// plus one platform event that carries no tenant at all. Resource names the
// owner so a result is compared by what it contains, independent of how each
// transport encodes the tenant field.
func seededAuditEvents() []audit.AuditEvent {
	base := time.Now().UTC().Add(-time.Hour)
	owners := []string{confineAcme, confineGlobex, tenantid.System, ""}
	events := make([]audit.AuditEvent, len(owners))
	for i, owner := range owners {
		// Distinct timestamps: the page cursor is a timestamp.
		events[i] = audit.AuditEvent{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Actor:     confineSeeder,
			TenantID:  owner,
			Resource:  "owner:" + resourceOwner(owner),
			Action:    "seed",
			Outcome:   audit.OutcomeSuccess,
		}
	}
	return events
}

func resourceOwner(tenantID string) string {
	if tenantID == "" {
		return "platform"
	}
	return tenantID
}

// bootConfinedCell boots a node over tk whose acting identity is the built-in
// Admin of callerTenant, with acme and globex created and the seeded events in
// the node's audit logger.
func bootConfinedCell(t *testing.T, tk transportKind, callerTenant string) confinedCell {
	t.Helper()
	ring := audit.NewRingBuffer(1024)
	var (
		booted  *node.Node
		adminID string
	)
	c := bootCellAs(t, tk, callerTenant, []server.ServerOption{server.WithAuditLogger(ring)}, func(n *node.Node) {
		booted = n
		adminID = seedConfinementFixtures(t, n, callerTenant)
	})
	for _, ev := range seededAuditEvents() {
		if err := ring.Emit(context.Background(), ev); err != nil {
			t.Fatalf("seed audit event: %v", err)
		}
	}

	ctx := boundedCtx(t)
	if tk == embeddedTransport {
		ctx = auth.WithIdentity(ctx, &auth.Identity{
			Subject:  "matrix",
			TenantID: callerTenant,
			Roles:    []string{adminID},
		})
	}
	return confinedCell{client: c, node: booted, ctx: ctx, callerTenant: callerTenant}
}

// seedConfinementFixtures creates acme and globex, and the "matrix" user the
// remote cells authenticate as, holding the built-in Admin role of
// callerTenant. It returns that role's id.
func seedConfinementFixtures(t *testing.T, n *node.Node, callerTenant string) string {
	t.Helper()
	svc := n.Services()
	ctx := boundedCtx(t)
	for _, id := range []string{confineAcme, confineGlobex} {
		if _, err := svc.Tenant.CreateTenant(ctx, id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}
	if _, err := svc.Role.ReconcileBuiltInRoles(ctx, callerTenant); err != nil {
		t.Fatalf("ReconcileBuiltInRoles(%s): %v", callerTenant, err)
	}
	adminID := role.SystemRoleID(callerTenant, role.RoleNameAdmin)
	if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "matrix",
		TenantID:             callerTenant,
		RoleIDs:              []string{adminID},
	}); err != nil {
		t.Fatalf("create user in %s: %v", callerTenant, err)
	}
	return adminID
}

// auditOwners pages through the seeded events f reaches, one event per page so
// the cursor is exercised on every step, and returns the owners it saw. Each
// event must also arrive carrying its own tenant: a transport that drops the
// field hides from its caller whose event it is.
func auditOwners(ctx context.Context, ac transport.AuditClient, f transport.AuditFilter) ([]string, error) {
	f.Actor = confineSeeder
	f.Limit = 1
	seen := map[string]bool{}
	for range 16 {
		page, err := ac.QueryAudit(ctx, f)
		if err != nil {
			return nil, err
		}
		for _, ev := range page.Events {
			owner := strings.TrimPrefix(ev.Resource, "owner:")
			if owner != resourceOwner(ev.TenantID) {
				return nil, fmt.Errorf("event %s arrived with tenant %q", ev.Resource, ev.TenantID)
			}
			seen[owner] = true
		}
		if page.Next == "" {
			break
		}
		f.Cursor = page.Next
	}
	owners := make([]string, 0, len(seen))
	for o := range seen {
		owners = append(owners, o)
	}
	sort.Strings(owners)
	return owners, nil
}

// requestStatus reports whether any *transport.RequestError in err's chain
// carries status. The SDK transports nest one RequestError inside another (the
// typed call wraps the wire call), so errors.As, which stops at the first,
// would read the outer one's zero status.
func requestStatus(err error, status int) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if re, ok := e.(*transport.RequestError); ok && re.StatusCode == status {
			return true
		}
	}
	return false
}

// crossTenantRefusals is what a cross-tenant request must get on each
// transport: permission denied, in that transport's typed form. Every
// transport is listed, so a new one declares its refusal instead of inheriting
// a default.
var crossTenantRefusals = map[transportKind]func(error) bool{
	embeddedTransport: func(err error) bool {
		var denied *tenant.CrossTenantDeniedError
		return errors.As(err, &denied)
	},
	restTransport: func(err error) bool {
		var unexpected *transport.UnexpectedStatusError
		if errors.As(err, &unexpected) && unexpected.StatusCode == http.StatusForbidden {
			return true
		}
		return requestStatus(err, http.StatusForbidden)
	},
	grpcTransport: func(err error) bool {
		st, ok := grpcStatusOf(err)
		return ok && st.Code() == codes.PermissionDenied
	},
	unixTransport: func(err error) bool {
		st, ok := grpcStatusOf(err)
		return ok && st.Code() == codes.PermissionDenied
	},
	quicTransport: func(err error) bool {
		return requestStatus(err, int(quicwire.StatusPermissionDenied))
	},
	mcpTransport: func(err error) bool {
		return requestStatus(err, int(mcpwire.CodePermissionDenied))
	},
}

// classifyCrossTenantRefusal returns nil when err is tk's cross-tenant
// refusal and a reason otherwise. A missing error, a timeout or an unrelated
// failure is not a refusal.
func classifyCrossTenantRefusal(tk transportKind, err error) error {
	if err == nil {
		return errors.New("the request succeeded")
	}
	refused, ok := crossTenantRefusals[tk]
	if !ok {
		return fmt.Errorf("%s declares no cross-tenant refusal", tk)
	}
	if !refused(err) {
		return fmt.Errorf("%T %v is not %s's permission-denied refusal", err, err, tk)
	}
	return nil
}

func assertCrossTenantRefused(t *testing.T, tk transportKind, verb string, err error) {
	t.Helper()
	if reason := classifyCrossTenantRefusal(tk, err); reason != nil {
		t.Errorf("%s: %s: %v", tk, verb, reason)
	}
}

// TestAuditQuery_TenantAdminConfinedToOwnTenant: acme's Admin reads acme's
// events and nothing else -- not by naming globex, not by naming the system
// tenant, and not by naming no tenant at all.
func TestAuditQuery_TenantAdminConfinedToOwnTenant(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootConfinedCell(t, tk, confineAcme)
			ac := c.client.Audit()
			if ac == nil {
				t.Fatalf("%s: no audit client", tk)
			}

			for _, f := range []transport.AuditFilter{{}, {TenantID: confineAcme}} {
				got, err := auditOwners(c.ctx, ac, f)
				if err != nil {
					t.Fatalf("%s: query %+v: %v", tk, f, err)
				}
				if want := []string{confineAcme}; !slices.Equal(got, want) {
					t.Errorf("%s: query %+v reached %v; want only %v", tk, f, got, want)
				}
			}

			for _, foreign := range []string{confineGlobex, tenantid.System} {
				_, err := auditOwners(c.ctx, ac, transport.AuditFilter{TenantID: foreign})
				assertCrossTenantRefused(t, tk, "audit query naming "+foreign, err)
			}
		})
	}
}

// TestAuditQuery_SystemAdminConfinedToPlatformEvents: the system tenant's
// Admin reads the system tenant's events and the platform events that carry no
// tenant, and is refused every other tenant's.
func TestAuditQuery_SystemAdminConfinedToPlatformEvents(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootConfinedCell(t, tk, tenantid.System)
			ac := c.client.Audit()
			if ac == nil {
				t.Fatalf("%s: no audit client", tk)
			}

			got, err := auditOwners(c.ctx, ac, transport.AuditFilter{})
			if err != nil {
				t.Fatalf("%s: query: %v", tk, err)
			}
			if want := []string{"platform", tenantid.System}; !slices.Equal(got, want) {
				t.Errorf("%s: an unfiltered query reached %v; want only %v", tk, got, want)
			}

			for _, foreign := range []string{confineAcme, confineGlobex} {
				_, err := auditOwners(c.ctx, ac, transport.AuditFilter{TenantID: foreign})
				assertCrossTenantRefused(t, tk, "audit query naming "+foreign, err)
			}
		})
	}
}
