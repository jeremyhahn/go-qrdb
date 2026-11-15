// Copyright (c) 2025 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// operatorAdmissionKey carries an admitted operator identity write: a
// platform operator, of its own tenant, admitted to write another tenant's
// users or roles under that tenant's operator-access policy.
type operatorAdmissionKey struct{}

type operatorAdmission struct {
	target, operatorTenant string
}

// WithOperatorIdentityWrite records in ctx that the principal, of tenant
// operatorTenant, was admitted (and the admission recorded) to write the
// identity records of tenant target. The middleware's operator-access gate
// sets it; nothing else may.
func WithOperatorIdentityWrite(ctx context.Context, target, operatorTenant string) context.Context {
	return context.WithValue(ctx, operatorAdmissionKey{}, operatorAdmission{
		target:         tenantid.Normalize(target),
		operatorTenant: tenantid.Normalize(operatorTenant),
	})
}

// GrantAuthorityTenant returns the tenant whose authority judges a grant
// the principal in ctx makes in target (role.ValidateGrantable): target
// itself, or, for an operator admitted to write target's identity records,
// the operator's own tenant. The operator holds no role in target; what it
// may grant there is what it holds where it is an operator, so the grant
// rule -- a grantor grants only what it holds -- still binds it.
func GrantAuthorityTenant(ctx context.Context, target string) string {
	if a, ok := ctx.Value(operatorAdmissionKey{}).(operatorAdmission); ok && a.target == tenantid.Normalize(target) {
		return a.operatorTenant
	}
	return target
}
