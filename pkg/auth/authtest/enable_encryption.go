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

package authtest

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// EnableEncryptionTenant is the tenant whose admin the enable-encryption
// scope cases hold tenant_barrier:enable_encryption in.
const EnableEncryptionTenant = "acme"

// EnableEncryptionOtherTenant is a tenant no enable-encryption scope case
// holds any permission in.
const EnableEncryptionOtherTenant = "globex"

// EnableEncryptionScopeCase is one caller of barrier enable-encryption, the
// barrier it names, and the verdict the per-scope rule gives it.
type EnableEncryptionScopeCase struct {
	// Name labels the case as a subtest.
	Name string
	// Caller is the authenticated identity making the call.
	Caller *auth.Identity
	// TenantID is the barrier the request names; empty is the node's own.
	TenantID string
	// Admit reports whether the call must reach the migrator.
	Admit bool
}

// EnableEncryptionScopeCases returns the callers every transport's
// enable-encryption authorization is tested against (go-qrdb#791). No tenant
// named is the node's own barrier, authorized by seal:enable_encryption in
// the system tenant; a named tenant is that tenant's barrier, authorized by
// tenant_barrier:enable_encryption for THAT tenant, the rule every other
// tenant-barrier operation uses. Holding one permission never stands in for
// the other.
func EnableEncryptionScopeCases() []EnableEncryptionScopeCase {
	operator := GrantedNonSOIdentity(tenantid.System, "operator", permission.SealEnableEncryption)
	admin := GrantedNonSOIdentity(EnableEncryptionTenant, "acme-admin", permission.TenantBarrierEnableEncryption)
	return []EnableEncryptionScopeCase{
		{Name: "operator names a tenant", Caller: operator, TenantID: EnableEncryptionTenant, Admit: false},
		{Name: "tenant admin names another tenant", Caller: admin, TenantID: EnableEncryptionOtherTenant, Admit: false},
		{Name: "tenant admin names its own tenant", Caller: admin, TenantID: EnableEncryptionTenant, Admit: true},
		{Name: "operator on the node barrier", Caller: operator, TenantID: "", Admit: true},
		{Name: "tenant admin on the node barrier", Caller: admin, TenantID: "", Admit: false},
	}
}
