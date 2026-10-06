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

package tenant

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// RefusalCodePoolUnavailable and RefusalCodeProviderMissing are the two
// stable TenantInfo.Refusal codes a transport may put on the wire
// (docs/volume-pools/api.md's TenantRefusal). They identify WHY a
// dedicated-isolation tenant's data was refused, never carry a path: the
// catalog's Assignment and its enclosing Pool are the only durable
// location records in this architecture (docs/volume-pools D9), so every
// refusal traces to one of the two.
const (
	// RefusalCodePoolUnavailable: the tenant's catalog Assignment did not
	// resolve on this node -- no volume allocator/resolver configured, no
	// Assignment for the tenant, the pool is not open on this node, or the
	// resolved path escaped the pool (a local invariant violation). The
	// pool, or this node's access to it, is the problem; another node may
	// still serve the tenant.
	RefusalCodePoolUnavailable = "pool_unavailable"

	// RefusalCodeProviderMissing: the pool names a volume.Provider this
	// node has not registered. Distinct from pool_unavailable because the
	// remedy differs: register the provider, not fix a mount.
	RefusalCodeProviderMissing = "provider_missing"
)

// Refusal is the wire form of TenantInfo.Refusal: a stable code and the
// underlying typed error's display text. Every transport converts through
// this one function rather than inspecting the error itself, so the
// code/message pair is identical on every wire.
type Refusal struct {
	// Code is one of the RefusalCode* constants above.
	Code string
	// Message is err.Error(), for display only -- never parsed.
	Message string
}

// RefusalOf classifies err (TenantInfo.Refusal) into its wire Refusal.
// Returns nil for a nil err, so a caller can set a response field
// unconditionally from ListTenants' per-tenant result.
func RefusalOf(err error) *Refusal {
	if err == nil {
		return nil
	}
	code := RefusalCodePoolUnavailable
	var notRegistered *volume.ProviderNotRegisteredError
	if errors.As(err, &notRegistered) {
		code = RefusalCodeProviderMissing
	}
	return &Refusal{Code: code, Message: err.Error()}
}
