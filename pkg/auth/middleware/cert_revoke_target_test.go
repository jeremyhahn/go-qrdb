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

package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestCertRevokeTarget is the one statement of what a revocation targets,
// shared by AuthorizeCertRevoke and the embedded transport.
func TestCertRevokeTarget(t *testing.T) {
	res := &stubSerialResolver{owners: map[string]string{"s-acme": "acme"}}

	target, perm, err := CertRevokeTarget(context.Background(), "s-acme", res)
	if err != nil || target != "acme" || perm != permission.CertsRevoke {
		t.Errorf("owned serial = (%q, %q, %v); want acme, certs:revoke", target, perm, err)
	}

	target, perm, err = CertRevokeTarget(context.Background(), "unknown", res)
	if err != nil || target != tenant.SystemTenantID || perm != permission.CertsRevokeCross {
		t.Errorf("unowned serial = (%q, %q, %v); want the system tenant, certs:revoke:cross", target, perm, err)
	}

	var unresolvable *CertSerialUnresolvableError
	if _, _, err := CertRevokeTarget(context.Background(), "s", nil); !errors.As(err, &unresolvable) {
		t.Errorf("nil resolver: err = %v; want *CertSerialUnresolvableError", err)
	}

	sentinel := errors.New("store is down")
	if _, _, err := CertRevokeTarget(context.Background(), "s", &stubSerialResolver{err: sentinel}); !errors.Is(err, sentinel) {
		t.Errorf("resolver error: err = %v; want it returned as-is", err)
	}
}
