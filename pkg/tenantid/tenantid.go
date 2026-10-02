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

// Package tenantid holds the reserved system tenant identifier and the
// structural rule for a key's "<tenant>/<db>/" namespace (NamespaceOf). It is
// a leaf package -- it imports nothing from this module -- so packages below
// pkg/tenant in the import graph (pkg/ceremony, pkg/auth, pkg/compaction) can
// use them without importing pkg/tenant, which imports them.
package tenantid

// System is the reserved identifier of the platform's own tenant.
// tenant.SystemTenantID is this constant; see it for the tenancy rules that
// attach to it.
const System = "system"
