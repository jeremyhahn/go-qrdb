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

func TestSDK_SpiffeIDURIRoundTripsThroughTenantFromCert(t *testing.T) {
	for _, id := range []sdk.SpiffeID{
		{TrustDomain: "db.example.com", TenantID: "acme", Kind: sdk.SpiffeKindUser, Name: "alice"},
		{TrustDomain: "db.example.com", TenantID: "acme", Kind: sdk.SpiffeKindService, Name: "billing"},
	} {
		u, err := id.URI()
		if err != nil {
			t.Fatalf("URI(%+v): %v", id, err)
		}
		got, err := sdk.TenantFromCert(&x509.Certificate{URIs: []*url.URL{u}}, "db.example.com")
		if err != nil || got != id {
			t.Fatalf("TenantFromCert = %+v, %v; want %+v", got, err, id)
		}
	}
}

func TestSDK_SpiffeIDURIRefusals(t *testing.T) {
	tests := []struct {
		name string
		id   sdk.SpiffeID
		want error
	}{
		{"empty trust domain", sdk.SpiffeID{TenantID: "acme", Kind: sdk.SpiffeKindUser, Name: "alice"}, sdk.ErrSpiffeTrustDomainEmpty},
		{"empty tenant", sdk.SpiffeID{TrustDomain: "db.example.com", Kind: sdk.SpiffeKindUser, Name: "alice"}, sdk.ErrSpiffeTenantEmpty},
		{"unknown kind", sdk.SpiffeID{TrustDomain: "db.example.com", TenantID: "acme", Kind: "robot", Name: "r2"}, sdk.ErrSpiffeKindInvalid},
		{"empty name", sdk.SpiffeID{TrustDomain: "db.example.com", TenantID: "acme", Kind: sdk.SpiffeKindUser}, sdk.ErrSpiffeNameEmpty},
		{"tenant with a separator", sdk.SpiffeID{TrustDomain: "db.example.com", TenantID: "acme/evil", Kind: sdk.SpiffeKindUser, Name: "alice"}, sdk.ErrSpiffeIDNotRoundTrip},
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

// ExampleSpiffeID_URI builds the URI SAN for a tenant user and reads it back
// off a certificate the way the server authenticates it.
func ExampleSpiffeID_URI() {
	id := sdk.SpiffeID{TrustDomain: "db.example.com", TenantID: "acme", Kind: sdk.SpiffeKindUser, Name: "alice"}
	san, err := id.URI()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(san)

	got, err := sdk.TenantFromCert(&x509.Certificate{URIs: []*url.URL{san}}, "db.example.com")
	fmt.Println(got.TenantID, got.Kind, got.Name, err)

	_, err = sdk.SpiffeID{TrustDomain: "db.example.com", TenantID: "acme/evil", Kind: sdk.SpiffeKindUser, Name: "alice"}.URI()
	fmt.Println(errors.Is(err, sdk.ErrSpiffeIDNotRoundTrip))
	// Output:
	// spiffe://db.example.com/tenant/acme/user/alice
	// acme user alice <nil>
	// true
}
