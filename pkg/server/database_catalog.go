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

package server

import "github.com/jeremyhahn/go-qrdb/pkg/database"

// managerCatalog adapts a database.Manager to the DatabaseCatalog interface so
// the REST service registry can resolve any persisted database to a namespaced
// view of the shared service set. It enforces the same per-tenant ownership
// semantics as the manager's *ForTenant operations: the system tenant sees
// databases with no owner or the system owner, and every other tenant sees only
// databases it owns.
type managerCatalog struct {
	manager      *database.Manager
	systemTenant string
}

// Compile-time interface check.
var _ DatabaseCatalog = (*managerCatalog)(nil)

// newManagerCatalog creates a DatabaseCatalog backed by the given manager.
func newManagerCatalog(manager *database.Manager, systemTenant string) *managerCatalog {
	return &managerCatalog{manager: manager, systemTenant: systemTenant}
}

// DatabaseExists reports whether a database with the given name exists and is
// accessible to the given tenant.
func (c *managerCatalog) DatabaseExists(tenantID, name string) bool {
	db, err := c.manager.Get(name)
	if err != nil {
		return false
	}
	owner := db.TenantID()
	if tenantID == c.systemTenant {
		return owner == "" || owner == c.systemTenant
	}
	return owner == tenantID
}
