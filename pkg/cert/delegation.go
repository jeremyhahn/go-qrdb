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

package cert

import (
	"crypto/x509/pkix"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// delegationExtension checks that req is a well-formed delegation request
// and returns the critical extension its certificate carries: the grant is
// valid, names req.TenantID, is not the system tenant's, and req.Username is
// the subject the authenticator gives the grant's principal. Every refusal
// is decided before the CA key is touched.
func delegationExtension(req *IssueRequest) (pkix.Extension, error) {
	g := req.Delegation
	ext, err := g.Extension()
	if err != nil {
		return pkix.Extension{}, err
	}
	if g.Tenant != req.TenantID {
		return pkix.Extension{}, &ValidationError{Field: "delegation.tenant", Reason: "must be the issuing tenant"}
	}
	if tenantid.IsSystem(g.Tenant) {
		return pkix.Extension{}, &ValidationError{Field: "delegation.tenant", Reason: "the system tenant cannot delegate"}
	}
	if req.Username != auth.ServicePrincipalSubjectPrefix+g.Principal {
		return pkix.Extension{}, &ValidationError{Field: "username", Reason: "must be the delegated principal's subject " + auth.ServicePrincipalSubjectPrefix + g.Principal}
	}
	return ext, nil
}

// delegationTTL is the lifetime a delegation certificate asks for: ttl, or
// delegation.DefaultLifetime when unset, never above delegation.MaxLifetime,
// which every verifier refuses to exceed.
func delegationTTL(ttl time.Duration) time.Duration {
	if ttl == 0 {
		return delegation.DefaultLifetime
	}
	return min(ttl, delegation.MaxLifetime)
}
