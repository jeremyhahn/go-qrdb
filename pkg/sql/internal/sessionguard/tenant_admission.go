//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
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

package sessionguard

import (
	"context"
	"errors"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// TenantAdmission returns the Admission of mw's tenant status gate, the one
// every SQL listener's guard is built with. A tenant is revoked once the chain
// refuses it as not active -- suspended or disabled -- and the guard closes
// its connections: a MySQL server-side cursor is fetched inside vitess, and a
// suspended PostgreSQL portal is resumed inside psql-wire, so what such a
// connection already holds would otherwise keep serving rows. A status the
// chain cannot read revokes nothing; the connection's next statement is
// refused for it.
func TenantAdmission(mw *authmw.AuthMiddleware) Admission {
	return tenantAdmission{mw: mw}
}

// tenantAdmission is the guard's view of the auth chain's tenant status gate
// (TenantAdmission).
type tenantAdmission struct {
	mw *authmw.AuthMiddleware
}

var _ Admission = tenantAdmission{}

// Revoked implements Admission.
func (a tenantAdmission) Revoked(ctx context.Context, tenantID string) bool {
	var notActive *authmw.TenantNotActiveError
	return errors.As(a.mw.AdmitTenant(ctx, tenantID), &notActive)
}
