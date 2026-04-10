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

package transport

import "strconv"

// TenantDatabaseAccessDeniedError is returned when a tenant attempts to
// access a database that belongs to a different tenant.
type TenantDatabaseAccessDeniedError struct {
	TenantID string
	Database string
}

// Error returns a human-readable description of the access denial.
func (e *TenantDatabaseAccessDeniedError) Error() string {
	return "transport: tenant " + strconv.Quote(e.TenantID) +
		" access denied for database " + strconv.Quote(e.Database)
}
