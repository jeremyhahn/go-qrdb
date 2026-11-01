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

// Package credentials re-exports go-qrdb's per-tenant named-credential store
// resolution so SDK consumers never import the internal pkg/service path. All
// public types and constructors are aliases — there is no logic in this file.
//
// A service-layer credential servicer (sdk.NewCredentialService) serves the
// system tenant from the node-wide store it is built with. Passing
// WithTenantBarriers binds the tenant barrier lookup through which every other
// tenant's caller reaches its own store, encrypted under its own barrier; a
// servicer built without it refuses tenant callers rather than serving them
// from the node-wide store.
//
// See github.com/jeremyhahn/go-qrdb/pkg/service for the canonical
// documentation.
package credentials

import "github.com/jeremyhahn/go-qrdb/pkg/service"

// TenantBarriers looks up a tenant's barrier; *sdk.BarrierRegistry satisfies
// it.
type TenantBarriers = service.CredentialTenantBarriers

// ServiceOption configures a service-layer credential servicer.
type ServiceOption = service.CredentialServiceOption

// WithTenantBarriers binds the tenant barrier lookup the credential verbs
// resolve a tenant caller's store through.
var WithTenantBarriers = service.WithCredentialTenantBarriers
