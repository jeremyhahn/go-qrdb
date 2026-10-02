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

package ceremony

import (
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// scopeKind distinguishes system-level, tenant-level, and recovery-level
// barrier scopes.
type scopeKind int

const (
	scopeSystem   scopeKind = iota
	scopeTenant             // per-tenant barrier
	scopeRecovery           // recovery key distribution
)

// BarrierScope identifies which barrier a ceremony or officer registry entry
// belongs to. System barriers use ScopeSystem; tenant barriers use
// ScopeTenant("<tenantID>"); recovery operations use ScopeRecovery.
type BarrierScope struct {
	kind     scopeKind
	tenantID string // non-empty only when kind == scopeTenant
}

// ScopeSystem is the barrier scope for the system (global) barrier.
var ScopeSystem = BarrierScope{kind: scopeSystem}

// ScopeRecovery is the barrier scope for recovery key distribution.
// Recovery officers may be a different set from the unseal officers.
var ScopeRecovery = BarrierScope{kind: scopeRecovery}

// ScopeTenant returns a barrier scope for the named tenant.
func ScopeTenant(tenantID string) BarrierScope {
	return BarrierScope{kind: scopeTenant, tenantID: tenantID}
}

// StoragePrefix returns the prefix used for this scope's officer registry
// entries in the storage backend.
//   - System:   "ceremony/officers/"
//   - Tenant:   "ceremony/tenants/<id>/officers/"
//   - Recovery: "ceremony/recovery/officers/"
func (b BarrierScope) StoragePrefix() string {
	switch b.kind {
	case scopeTenant:
		return "ceremony/tenants/" + b.tenantID + "/officers/"
	case scopeRecovery:
		return "ceremony/recovery/officers/"
	default:
		return "ceremony/officers/"
	}
}

// TenantID returns the tenant this scope's officer certificates authenticate
// in: the tenant itself for a tenant scope, and the system tenant for the
// node ceremony and recovery scopes, which belong to the platform.
func (b BarrierScope) TenantID() string {
	if b.kind == scopeTenant {
		return b.tenantID
	}
	return tenantid.System
}

// String returns a human-readable description of the scope for diagnostics.
func (b BarrierScope) String() string {
	switch b.kind {
	case scopeTenant:
		return "tenant:" + b.tenantID
	case scopeRecovery:
		return "recovery"
	default:
		return "system"
	}
}

// LogValue returns a slog.Value for structured logging.
func (b BarrierScope) LogValue() slog.Value {
	switch b.kind {
	case scopeTenant:
		return slog.GroupValue(
			slog.String("kind", "tenant"),
			slog.String("tenant_id", b.tenantID),
		)
	case scopeRecovery:
		return slog.StringValue("recovery")
	default:
		return slog.StringValue("system")
	}
}
