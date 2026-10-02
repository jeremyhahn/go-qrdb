// Copyright (c) 2026 Jeremy Hahn
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

package middleware

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The certificate-authority tenancy rule, in one place for every transport.
//
// The certs surface is the shape a route permission cannot gate on its own: a
// single process-wide CertProvider serves every tenant, and three of its four
// verbs name their target in the REQUEST — tenantID in the generate body,
// tenantID in the list query, and, for revoke, a bare serial that resolves to
// a tenant only by looking it up. A route permission is checked against the
// caller's own tenant, so on its own it says nothing at all about which
// tenant's certificates the call reaches.
//
// The functions here answer that second question. They sit beside
// AuthorizeTenantBarrierInit for the same reason it exists: REST and gRPC must
// not each carry their own copy of the rule, because a rule kept in two places
// is a rule that will hold in one of them.

// CertSerialResolver reports which tenant owns an issued certificate serial.
// *cert.DefaultCertProvider implements it; the interface is declared here, at
// the consumer, so this package does not depend on pkg/cert.
type CertSerialResolver interface {
	// TenantForSerial returns the owning tenant for serial and whether this
	// node holds an issued record for it at all.
	TenantForSerial(ctx context.Context, serial string) (tenantID string, found bool, err error)
}

// AuthorizeCertTenant authorizes a certificate operation against the tenant
// the REQUEST names — the tenantID in a generate body or a list query — rather
// than the tenant the caller happens to be in.
//
// It is AuthorizeTenant under a name that says why it is being called. The
// certs permissions are ScopeSelfStrict, so it admits the caller only when the
// request's target is the caller's own tenant -- for EVERY caller. The
// system-tenant rule does not lift them: a certificate this CA signs
// authenticates as the user and tenant its Subject names, so a system Admin
// minting one for another tenant's user would be logging in as that user.
//
// Passing the caller's own tenant here is not a no-op even though it looks
// like one — it is the statement that the handler CHECKED, which is precisely
// what was missing.
func (m *AuthMiddleware) AuthorizeCertTenant(ctx context.Context, targetTenantID string, perm permission.Permission) error {
	return m.AuthorizeTenant(ctx, targetTenantID, perm)
}

// AuthorizeCertRevoke authorizes a revocation of a single serial.
//
// Revocation is the verb whose target is not in the request: RevokeCert takes
// a serial, and a serial carries no tenant. So the target is RESOLVED first,
// and the answer selects which of two different authorities is required.
//
//   - The serial resolves to an issued record. Its record names the owning
//     tenant, so the ordinary self-scope permission.CertsRevoke is checked
//     against THAT tenant. A tenant admin revoking its own certificate is
//     admitted; anyone reaching for another tenant's serial is refused, the
//     platform operator included (certs:revoke is ScopeSelfStrict).
//
//   - The serial resolves to nothing. This is deny-by-serial, which
//     docs/certificate-authority/architecture.md guarantees and go-qrdb#287
//     confirmed, and it stays — but it requires permission.CertsRevokeCross,
//     which is ScopeCross and therefore system-tenant only. An unresolvable
//     serial names no tenant, so there is no claim a tenant-local caller could
//     make on it, and the marker it writes is read by every tenant. Admitting
//     it on a self-scope permission is what made "revoke another tenant's
//     certificate" and "revoke a peer-issued certificate" the same request.
//
// A resolver error is returned as-is and the caller must refuse: the tenancy
// of the operation is unknown, and an unknown target is not an absent one.
func (m *AuthMiddleware) AuthorizeCertRevoke(ctx context.Context, serial string, resolver CertSerialResolver) error {
	identity := auth.IdentityFromContext(ctx)
	if identity == nil {
		return auth.ErrUnauthenticated
	}
	target, perm, err := CertRevokeTarget(ctx, serial, resolver)
	if err != nil {
		return err
	}
	if perm == permission.CertsRevokeCross {
		// No owning tenant: the CA-operator authority, evaluated against the
		// caller's own tenant because ScopeCross ignores the target anyway.
		return m.authorize(ctx, identity, perm)
	}
	return m.AuthorizeTenant(ctx, target, perm)
}

// CertRevokeTarget establishes what a revocation of serial is authorized as,
// for every transport: the tenant that owns the serial and
// permission.CertsRevoke when resolver holds an issued record for it, or the
// system tenant and permission.CertsRevokeCross -- the deny-by-serial
// CA-operator act -- when it holds none. AuthorizeCertRevoke decides the
// network transports' revocations on it, and the embedded transport, which
// has no middleware, confines its caller to the same target.
//
// A nil resolver is a *CertSerialUnresolvableError and a resolver error is
// returned as-is: an unknown target is not an absent one, so the caller must
// refuse.
func CertRevokeTarget(ctx context.Context, serial string, resolver CertSerialResolver) (tenantID string, perm permission.Permission, err error) {
	if resolver == nil {
		return "", "", &CertSerialUnresolvableError{Serial: serial}
	}
	owner, found, err := resolver.TenantForSerial(ctx, serial)
	if err != nil {
		return "", "", err
	}
	if !found {
		return tenantid.System, permission.CertsRevokeCross, nil
	}
	return owner, permission.CertsRevoke, nil
}
