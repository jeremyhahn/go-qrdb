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

package sdk_test

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

func TestSDK_DelegationRefusals(t *testing.T) {
	if _, err := (sdk.DelegationGrant{Tenant: "acme", Principal: "billing"}).Extension(); !errors.As(err, new(*sdk.DelegationGrantError)) {
		t.Fatalf("an empty scope = %v, want *DelegationGrantError", err)
	}
	ext, err := sdk.DelegationGrant{Tenant: "acme", Principal: "billing", Scope: []string{"kv:get"}}.Extension()
	if err != nil {
		t.Fatalf("Extension: %v", err)
	}
	ext.Critical = false
	if _, found, err := sdk.DelegationFromCertificate(&x509.Certificate{Extensions: []pkix.Extension{ext}}); !found || !errors.As(err, new(*sdk.DelegationExtensionError)) {
		t.Fatalf("a non-critical extension: found=%v err=%v, want *DelegationExtensionError", found, err)
	}
	now := time.Now()
	long := &x509.Certificate{NotBefore: now, NotAfter: now.Add(sdk.DelegationMaxLifetime + time.Second)}
	if err := sdk.DelegationCheckLifetime(long); !errors.As(err, new(*sdk.DelegationLifetimeError)) {
		t.Fatalf("an over-long lifetime = %v, want *DelegationLifetimeError", err)
	}
	short := &x509.Certificate{NotBefore: now, NotAfter: now.Add(sdk.DelegationDefaultLifetime)}
	if err := sdk.DelegationCheckLifetime(short); err != nil {
		t.Fatalf("the default lifetime = %v, want nil", err)
	}
	if sdk.DelegationMaxScope < 1 {
		t.Fatalf("DelegationMaxScope = %d, want positive", sdk.DelegationMaxScope)
	}
}
