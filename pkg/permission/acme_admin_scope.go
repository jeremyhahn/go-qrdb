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

package permission

// ---------------------------------------------------------------------------
// ACME server administration and CA hierarchy permissions
//
// These canonical resource:action constants let qrdb serve as the single RBAC
// policy decision point for the RFC 8555 ACME *administration* surface and for
// certificate-authority *hierarchy* management. Both capabilities live above
// qrdb's own engine (in go-xkms's TrustedCA / ACME admin API), but the stack
// enforces them through qrdb's canonical vocabulary rather than a consumer's
// private role model.
//
// Reconciliation rule (same as broker_scope.go): qrdb OWNS the resource names.
// Nothing here duplicates an existing qrdb resource:
//
//   - acme_accounts / acme_orders / acme_eab
//     RFC 8555 registration objects. They are neither certificates (certs:*)
//     nor platform users (users:*): an ACME account is a JWK-keyed protocol
//     registration, an order is an issuance request, and an External Account
//     Binding is a pre-shared enrolment credential. Separating the three
//     resources is what lets a deployment mint a narrow "endorser" role that
//     holds acme_eab:create and nothing else.
//
//   - ca
//     Certificate-authority hierarchy lifecycle: creating a root or
//     intermediate CA, listing and reading CAs, changing a CA's status, and
//     selecting a tenant's default issuing CA. This is strictly more
//     privileged than certs:* (which is *leaf* certificate lifecycle:
//     generate / revoke / read / list / export). Holding certs:generate must
//     not imply the authority to mint a new signing CA.
//
// These permissions are deliberately NOT registered as ScopeTenantOnly (the
// scope carried by the broker's acme:read / acme:write, which model a tenant's
// own ACME issuance settings and are denied to the platform operator by
// design). Administering the ACME server and its CA hierarchy IS a platform
// capability: they are declared ScopeSelf in self_scope.go, so the system-tenant rule lifts
// them across tenants for a system-tenant Admin while a tenant-local Admin
// stays confined to their own tenant.
// ---------------------------------------------------------------------------

const (
	// ACMEAccountsRead grants read access to a single ACME account's
	// registration state.
	ACMEAccountsRead Permission = "acme_accounts:read"
	// ACMEAccountsList grants access to enumerate ACME accounts.
	ACMEAccountsList Permission = "acme_accounts:list"
	// ACMEAccountsDisable grants access to deactivate an ACME account,
	// revoking its ability to place further orders.
	ACMEAccountsDisable Permission = "acme_accounts:disable"
)

const (
	// ACMEOrdersRead grants read access to a single ACME order.
	ACMEOrdersRead Permission = "acme_orders:read"
	// ACMEOrdersList grants access to enumerate ACME orders.
	ACMEOrdersList Permission = "acme_orders:list"
)

const (
	// ACMEEABRead grants read access to a single External Account Binding's
	// metadata. It never exposes the EAB MAC key, which is returned only at
	// creation time.
	ACMEEABRead Permission = "acme_eab:read"
	// ACMEEABList grants access to enumerate External Account Bindings.
	ACMEEABList Permission = "acme_eab:list"
	// ACMEEABCreate grants access to mint an External Account Binding — the
	// credential that authorizes an external party to enrol with the ACME
	// server. This is the "endorser" authority.
	ACMEEABCreate Permission = "acme_eab:create"
	// ACMEEABDelete grants access to revoke an External Account Binding.
	ACMEEABDelete Permission = "acme_eab:delete"
)

const (
	// CACreate grants access to create a certificate authority in the
	// hierarchy — a root CA or an intermediate CA. Strictly more privileged
	// than CertsGenerate, which issues leaf certificates from an existing CA.
	CACreate Permission = "ca:create"
	// CARead grants read access to a certificate authority's metadata and to
	// the hierarchy's status.
	CARead Permission = "ca:read"
	// CAList grants access to enumerate the certificate authorities in the
	// hierarchy.
	CAList Permission = "ca:list"
	// CAUpdate grants access to change a certificate authority's status
	// (active / pending-rotation / revoked) and to select the default issuing
	// CA.
	CAUpdate Permission = "ca:update"
)

// acmeAdminPermissions returns every ACME-administration and CA-hierarchy
// permission declared in this file as a single slice. The init function below
// registers each one in the global registry.
func acmeAdminPermissions() []Permission {
	return []Permission{
		ACMEAccountsRead, ACMEAccountsList, ACMEAccountsDisable,
		ACMEOrdersRead, ACMEOrdersList,
		ACMEEABRead, ACMEEABList, ACMEEABCreate, ACMEEABDelete,
		CACreate, CARead, CAList, CAUpdate,
	}
}

// acmeAdminCategories groups the ACME-administration and CA-hierarchy
// permissions for UI rendering.
func acmeAdminCategories() []Category {
	return []Category{
		{
			Name:  "ACME Accounts",
			Group: "Security Services",
			Permissions: []Permission{
				ACMEAccountsRead, ACMEAccountsList, ACMEAccountsDisable,
			},
		},
		{
			Name:  "ACME Orders",
			Group: "Security Services",
			Permissions: []Permission{
				ACMEOrdersRead, ACMEOrdersList,
			},
		},
		{
			Name:  "ACME External Account Bindings",
			Group: "Security Services",
			Permissions: []Permission{
				ACMEEABRead, ACMEEABList, ACMEEABCreate, ACMEEABDelete,
			},
		},
		{
			Name:  "Certificate Authorities",
			Group: "Security Services",
			Permissions: []Permission{
				CACreate, CARead, CAList, CAUpdate,
			},
		},
	}
}

func init() {
	Register(acmeAdminPermissions()...)
	categories = append(categories, acmeAdminCategories()...)
}
