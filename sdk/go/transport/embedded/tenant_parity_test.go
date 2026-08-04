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

package embedded

import (
	"context"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// fillNonZeroDTO sets every settable int64 field of the struct to a distinct
// non-zero value, every bool to true, and every []string to a one-element
// slice. A field that a transport conversion silently drops therefore surfaces
// as a zero/empty value after a round trip.
func fillNonZeroDTO(rv reflect.Value) {
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.Int64:
			f.SetInt(int64(i + 1))
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			if f.Type().Elem().Kind() == reflect.String {
				f.Set(reflect.ValueOf([]string{"v"}))
			}
		}
	}
}

// TestEmbeddedTenantQuotas_RoundTripParity guards transport parity for tenant
// quotas: a fully populated DTO must survive the DTO -> domain -> DTO round trip
// through the embedded transport with every field intact. A quota field added
// to the DTO but not wired into both conversion directions surfaces here as a
// dropped (zero) value — the exact drift that let MaxDedicatedWALGroups reach
// only the gRPC and QUIC transports.
func TestEmbeddedTenantQuotas_RoundTripParity(t *testing.T) {
	var in transport.TenantQuotas
	fillNonZeroDTO(reflect.ValueOf(&in).Elem())

	tr := newExtTransport(t, WithTenantService(&mockTenantService{}))
	resp, err := tr.UpdateTenantQuotas(context.Background(), &transport.UpdateTenantQuotasRequest{
		TenantID: "t1",
		Quotas:   in,
	})
	if err != nil {
		t.Fatalf("UpdateTenantQuotas: %v", err)
	}
	if resp.Tenant.Quotas == nil {
		t.Fatal("response quotas nil")
	}
	if !reflect.DeepEqual(in, *resp.Tenant.Quotas) {
		t.Errorf("quota round trip dropped a field:\n in  = %+v\n out = %+v", in, *resp.Tenant.Quotas)
	}
}

// TestEmbeddedTenantEntitlements_RoundTripParity is the entitlements analogue of
// TestEmbeddedTenantQuotas_RoundTripParity.
func TestEmbeddedTenantEntitlements_RoundTripParity(t *testing.T) {
	var in transport.TenantEntitlements
	fillNonZeroDTO(reflect.ValueOf(&in).Elem())

	tr := newExtTransport(t, WithTenantService(&mockTenantService{}))
	resp, err := tr.UpdateTenantEntitlements(context.Background(), &transport.UpdateTenantEntitlementsRequest{
		TenantID:     "t1",
		Entitlements: in,
	})
	if err != nil {
		t.Fatalf("UpdateTenantEntitlements: %v", err)
	}
	if resp.Tenant.Entitlements == nil {
		t.Fatal("response entitlements nil")
	}
	if !reflect.DeepEqual(in, *resp.Tenant.Entitlements) {
		t.Errorf("entitlement round trip dropped a field:\n in  = %+v\n out = %+v", in, *resp.Tenant.Entitlements)
	}
}
