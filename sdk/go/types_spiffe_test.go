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
	"errors"
	"fmt"
	"net/url"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

func TestSDK_SpiffeIDURIRefusals(t *testing.T) {
	tests := []struct {
		name string
		id   sdk.SpiffeID
		want error
	}{
		{"empty trust domain", sdk.SpiffeID{Kind: sdk.SpiffeKindUser, Name: "alice"}, sdk.ErrSpiffeTrustDomainEmpty},
		{"trust domain not a name", sdk.SpiffeID{TrustDomain: "Acme", Kind: sdk.SpiffeKindUser, Name: "alice"}, sdk.ErrSpiffeTrustDomainInvalid},
		{"unknown kind", sdk.SpiffeID{TrustDomain: "acme", Kind: "robot", Name: "r2"}, sdk.ErrSpiffeKindInvalid},
		{"empty name", sdk.SpiffeID{TrustDomain: "acme", Kind: sdk.SpiffeKindUser}, sdk.ErrSpiffeNameEmpty},
		{"name with a separator", sdk.SpiffeID{TrustDomain: "acme", Kind: sdk.SpiffeKindUser, Name: "alice/evil"}, sdk.ErrSpiffePathInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := tc.id.URI()
			var refused *sdk.SpiffeIDError
			if !errors.As(err, &refused) || !errors.Is(err, tc.want) || u != nil {
				t.Fatalf("URI = %v, %v; want a *SpiffeIDError wrapping %v", u, err, tc.want)
			}
		})
	}
}

func TestSDK_WorkloadFromCertRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  []string
		want error
	}{
		"no SAN":          {nil, sdk.ErrSpiffeNoTenantID},
		"query":           {[]string{"spiffe://acme/user/alice?x=1"}, sdk.ErrSpiffeURIInvalid},
		"old tenant path": {[]string{"spiffe://db.example.com/tenant/acme/user/alice"}, sdk.ErrSpiffePathInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			c := &x509.Certificate{}
			for _, raw := range tc.raw {
				u, err := url.Parse(raw)
				if err != nil {
					t.Fatal(err)
				}
				c.URIs = append(c.URIs, u)
			}
			if _, err := sdk.WorkloadFromCert(c); !errors.Is(err, tc.want) {
				t.Fatalf("WorkloadFromCert = %v, want %v", err, tc.want)
			}
		})
	}
}

// ExampleSpiffeID_URI builds the URI SAN for a tenant user and reads it back
// off a certificate the way the server authenticates it.
func ExampleSpiffeID_URI() {
	id := sdk.SpiffeID{TrustDomain: "acme", Kind: sdk.SpiffeKindUser, Name: "alice"}
	san, err := id.URI()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(san)

	got, err := sdk.WorkloadFromCert(&x509.Certificate{URIs: []*url.URL{san}})
	fmt.Println(got.TrustDomain, got.Kind, got.Name, err)

	_, err = sdk.SpiffeID{TrustDomain: "acme", Kind: sdk.SpiffeKindUser, Name: "alice/evil"}.URI()
	fmt.Println(errors.Is(err, sdk.ErrSpiffePathInvalid))
	// Output:
	// spiffe://acme/user/alice
	// acme user alice <nil>
	// true
}
