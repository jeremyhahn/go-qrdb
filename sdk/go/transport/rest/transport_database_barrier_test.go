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
	"errors"
	"net/http"
	"testing"
	"time"
)

// TestRESTDatabaseBarrier_SendsTheDocumentedRequest pins SealDatabase and
// DatabaseBarrierStatus to their verb, path and body on the wire, and checks
// the status answer is converted: the RFC 3339 initialisation time parsed and
// an unparsable one left zero. UnsealDatabase sends a secret, so it needs TLS
// and is pinned by the secret-guard tests.
func TestRESTDatabaseBarrier_SendsTheDocumentedRequest(t *testing.T) {
	ts := time.Date(2026, 2, 1, 8, 30, 0, 0, time.UTC)

	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "SealDatabase", verb: http.MethodPost, path: "/v1/databases/orders/seal", wantBody: struct{}{},
			status: http.StatusNoContent,
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.SealDatabase(ctx, "orders")
			}},
		{name: "DatabaseBarrierStatus", verb: http.MethodGet, path: "/v1/databases/orders/barrier/status",
			answer: restDatabaseBarrierStatus{Sealed: true, Strategy: "passphrase", HardwareBacked: true,
				InitializedAt: ts.Format(time.RFC3339)},
			callCheck: func(ctx context.Context, rt *Transport) error {
				st, err := rt.DatabaseBarrierStatus(ctx, "orders")
				if err == nil && (!st.Sealed || st.Strategy != "passphrase" || !st.HardwareBacked || !st.InitializedAt.Equal(ts)) {
					return errors.New("barrier status not converted, want sealed passphrase initialised at " + ts.String())
				}
				return err
			}},
		{name: "DatabaseBarrierStatus/UnparsableTimestamp", verb: http.MethodGet, path: "/v1/databases/orders/barrier/status",
			answer: restDatabaseBarrierStatus{Strategy: "none", InitializedAt: "not-a-time"},
			callCheck: func(ctx context.Context, rt *Transport) error {
				st, err := rt.DatabaseBarrierStatus(ctx, "orders")
				if err == nil && (st.Strategy != "none" || !st.InitializedAt.IsZero()) {
					return errors.New("want strategy none with a zero initialisation time")
				}
				return err
			}},
	})
}

// TestRESTDatabaseBarrier_SurfacesAFailedAnswer checks a refused or
// undecodable database barrier answer surfaces as a RequestError naming it.
func TestRESTDatabaseBarrier_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"SealDatabase", http.MethodPost, "/v1/databases/orders/seal", false, func(ctx context.Context, rt *Transport) error {
			return rt.SealDatabase(ctx, "orders")
		}},
		{"DatabaseBarrierStatus", http.MethodGet, "/v1/databases/orders/barrier/status", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.DatabaseBarrierStatus(ctx, "orders")
			return err
		}},
	})
}

// TestRESTDatabaseBarrier_Closed checks every database barrier RPC refuses on
// a closed transport before anything reaches the wire.
func TestRESTDatabaseBarrier_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"SealDatabase": func(ctx context.Context, rt *Transport) error {
			return rt.SealDatabase(ctx, "orders")
		},
		"UnsealDatabase": func(ctx context.Context, rt *Transport) error {
			return rt.UnsealDatabase(ctx, "orders", "secret")
		},
		"DatabaseBarrierStatus": func(ctx context.Context, rt *Transport) error {
			_, err := rt.DatabaseBarrierStatus(ctx, "orders")
			return err
		},
	})
}
