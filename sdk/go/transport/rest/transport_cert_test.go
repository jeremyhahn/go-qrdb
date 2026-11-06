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

package rest

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTCert_SendsTheDocumentedRequest pins the revoke and list
// operations' verb, path, query and body on the wire, and the decoded answer.
// IssueCert and EnrollCert are pinned field by field in
// transport_cert_enrollment_test.go.
func TestRESTCert_SendsTheDocumentedRequest(t *testing.T) {
	revoke := func(name, serial string, recordFound bool) wireCase {
		return wireCase{name: "RevokeCert/" + name, verb: http.MethodPost, path: "/certs/revoke",
			wantBody: map[string]string{"serial": serial},
			answer:   map[string]any{"status": "ok", "recordFound": recordFound},
			callCheck: func(ctx context.Context, rt *Transport) error {
				got, err := rt.RevokeCert(ctx, serial)
				if err != nil {
					return err
				}
				if want := (transport.RevokeResult{Serial: serial, RecordFound: recordFound}); *got != want {
					return fmt.Errorf("RevokeCert = %+v, want %+v", *got, want)
				}
				return nil
			}}
	}

	listCerts := func(name, tenantID, rawQuery string) wireCase {
		return wireCase{name: "ListCerts/" + name, verb: http.MethodGet, path: "/certs/list", rawQuery: rawQuery,
			answer: transport.ListCertsResponse{Certs: []*transport.CertRecord{{Serial: "001"}}},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.ListCerts(ctx, tenantID)
				if err != nil {
					return err
				}
				if len(resp.Certs) != 1 || resp.Certs[0].Serial != "001" {
					return fmt.Errorf("certs decoded as %+v, want serial 001", resp.Certs)
				}
				return nil
			}}
	}

	assertSendsTheDocumentedRequest(t, []wireCase{
		revoke("Recorded", "001", true),
		// A serial with no enrolment record is still denied by serial; the
		// answer says no record was found.
		revoke("DenyBySerial", "peer-serial", false),
		listCerts("Tenant", "a/b", "tenantID=a%2Fb"),
		listCerts("NoTenant", "", ""),
	})
}

// TestRESTCert_SurfacesAFailedAnswer checks the revoke and list operations
// turn a 5xx or a malformed body into a RequestError.
func TestRESTCert_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"RevokeCert", http.MethodPost, "/certs/revoke", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.RevokeCert(ctx, "001")
			return err
		}},
		{"ListCerts", http.MethodGet, "/certs/list", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListCerts(ctx, "t1")
			return err
		}},
	})
}

// TestRESTCert_Closed checks every certificate operation refuses on a closed
// transport before it reaches the wire.
func TestRESTCert_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(ctx context.Context, rt *Transport) error{
		"IssueCert": func(ctx context.Context, rt *Transport) error {
			_, err := rt.IssueCert(ctx, &transport.IssueCertRequest{})
			return err
		},
		"EnrollCert": func(ctx context.Context, rt *Transport) error {
			_, err := rt.EnrollCert(ctx, &transport.EnrollCertRequest{})
			return err
		},
		"RevokeCert": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RevokeCert(ctx, "001")
			return err
		},
		"ListCerts": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListCerts(ctx, "t1")
			return err
		},
	})
}
