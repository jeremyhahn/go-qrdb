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

// TestRESTFactor_SendsTheDocumentedRequest pins ListFactors' query, with
// and without a subject. The enrol, challenge, verify, list and revoke
// flow runs against the real REST server in
// TestAuthFactorRoundTrip_EnrollChallengeVerifyListRevoke.
func TestRESTFactor_SendsTheDocumentedRequest(t *testing.T) {
	listFactors := func(name string, req *transport.ListFactorsRequest, rawQuery string) wireCase {
		return wireCase{name: "ListFactors/" + name, verb: http.MethodGet, path: "/v1/authfactors/list",
			rawQuery: rawQuery, answer: transport.ListFactorsResponse{Total: 1},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.ListFactors(ctx, req)
				if err == nil && resp.Total != 1 {
					return fmt.Errorf("ListFactors total = %d, want 1", resp.Total)
				}
				return err
			}}
	}
	assertSendsTheDocumentedRequest(t, []wireCase{
		listFactors("Subject", &transport.ListFactorsRequest{Subject: "alice"}, "subject=alice"),
		listFactors("NilRequest", nil, ""),
	})
}

// TestRESTFactor_Closed checks every auth-factor operation refuses on a
// closed transport before it reaches the wire.
func TestRESTFactor_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(ctx context.Context, rt *Transport) error{
		"EnrollFactor": func(ctx context.Context, rt *Transport) error {
			_, err := rt.EnrollFactor(ctx, &transport.EnrollFactorRequest{})
			return err
		},
		"ChallengeFactor": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{})
			return err
		},
		"VerifyFactor": func(ctx context.Context, rt *Transport) error {
			return rt.VerifyFactor(ctx, &transport.VerifyFactorRequest{})
		},
		"ListFactors": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListFactors(ctx, &transport.ListFactorsRequest{})
			return err
		},
		"RevokeFactor": func(ctx context.Context, rt *Transport) error {
			return rt.RevokeFactor(ctx, &transport.RevokeFactorRequest{})
		},
	})
}
