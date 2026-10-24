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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/tenant"

// The tenant status-change auditor seam is reachable through the SDK as the
// pkg/tenant names themselves: a pointer to an interface converts only to a
// pointer to the identical interface, so these fail to compile unless each
// SDK name is an alias, and the option keeps the pkg/tenant signature.
var (
	_ *tenant.StatusChangeAuditor                                 = (*TenantStatusChangeAuditor)(nil)
	_ *tenant.StatusAuditWiring                                   = (*TenantStatusAuditWiring)(nil)
	_ func(tenant.StatusChangeAuditor) tenant.TenantServiceOption = WithTenantStatusChangeAuditor
	_ TenantStatusAuditWiring                                     = (*tenant.TenantService)(nil)
)
