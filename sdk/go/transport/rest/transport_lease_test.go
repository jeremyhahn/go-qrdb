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
	"fmt"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// checkLease reports how got differs from the lease leaseAnswer describes.
func checkLease(got *transport.LeaseInfo) error {
	if got.ID != 42 || got.TTL != 60 || got.GrantedTTL != 60 || got.RemainingTTL != 30 ||
		len(got.Keys) != 1 || string(got.Keys[0]) != "k1" {
		return fmt.Errorf("lease decoded as %+v, want id 42, ttl 60/60/30, keys [k1]", got)
	}
	return nil
}

// leaseAnswer is a lease as the server puts it on the wire.
var leaseAnswer = leaseWireResponse{ID: 42, TTL: 60, GrantedTTL: 60, RemainingTTL: 30, Keys: []string{"k1"}}

// TestRESTLease_SendsTheDocumentedRequest checks each lease RPC's verb,
// path, query and body, and that the answer decodes into the result.
func TestRESTLease_SendsTheDocumentedRequest(t *testing.T) {
	leaseID := map[string]any{"lease_id": 42}

	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "LeaseGrant", verb: http.MethodPost, path: "/lease/grant", wantBody: map[string]any{"ttl": 60}, answer: leaseAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.LeaseGrant(ctx, 60)
				if err != nil {
					return err
				}
				return checkLease(info)
			}},
		{name: "LeaseRevoke", verb: http.MethodPost, path: "/lease/revoke", wantBody: leaseID, answer: struct{}{},
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.LeaseRevoke(ctx, 42)
			}},
		{name: "LeaseKeepAlive", verb: http.MethodPost, path: "/lease/keepalive", wantBody: leaseID, answer: leaseAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.LeaseKeepAlive(ctx, 42)
				if err != nil {
					return err
				}
				return checkLease(info)
			}},
		{name: "LeaseTimeToLive", verb: http.MethodGet, path: "/lease/ttl", rawQuery: "lease_id=42", answer: leaseAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.LeaseTimeToLive(ctx, 42)
				if err != nil {
					return err
				}
				return checkLease(info)
			}},
		{name: "LeaseList", verb: http.MethodGet, path: "/lease/list",
			answer: leasesWireResponse{Leases: []*leaseWireResponse{&leaseAnswer, {ID: 2, TTL: 120}}},
			callCheck: func(ctx context.Context, rt *Transport) error {
				leases, err := rt.LeaseList(ctx)
				if err != nil {
					return err
				}
				if len(leases) != 2 || leases[1].ID != 2 || leases[1].Keys != nil {
					return fmt.Errorf("got %d leases, want 2, the second id 2 with no keys", len(leases))
				}
				return checkLease(leases[0])
			}},
	})
}

// TestRESTLease_SurfacesAFailedAnswer checks that each lease RPC reports a
// failed answer as a RequestError naming it.
func TestRESTLease_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"LeaseGrant", http.MethodPost, "/lease/grant", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.LeaseGrant(ctx, 60)
			return err
		}},
		{"LeaseRevoke", http.MethodPost, "/lease/revoke", false, func(ctx context.Context, rt *Transport) error {
			return rt.LeaseRevoke(ctx, 42)
		}},
		{"LeaseKeepAlive", http.MethodPost, "/lease/keepalive", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.LeaseKeepAlive(ctx, 42)
			return err
		}},
		{"LeaseTimeToLive", http.MethodGet, "/lease/ttl", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.LeaseTimeToLive(ctx, 42)
			return err
		}},
		{"LeaseList", http.MethodGet, "/lease/list", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.LeaseList(ctx)
			return err
		}},
	})
}

// TestRESTLease_TimeToLive_NotFound checks that a 404 for a lease's TTL
// reports the lease as not found, naming it.
func TestRESTLease_TimeToLive_NotFound(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/lease/ttl"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := rt.LeaseTimeToLive(context.Background(), 42)
	var lne *transport.LeaseNotFoundError
	if !errors.As(err, &lne) || lne.LeaseID != 42 {
		t.Fatalf("err = %v, want LeaseNotFoundError for lease 42", err)
	}
}

// TestRESTLease_Closed checks that every lease RPC on a closed transport
// refuses before the wire.
func TestRESTLease_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(ctx context.Context, rt *Transport) error{
		"LeaseGrant": func(ctx context.Context, rt *Transport) error {
			_, err := rt.LeaseGrant(ctx, 60)
			return err
		},
		"LeaseRevoke": func(ctx context.Context, rt *Transport) error {
			return rt.LeaseRevoke(ctx, 42)
		},
		"LeaseKeepAlive": func(ctx context.Context, rt *Transport) error {
			_, err := rt.LeaseKeepAlive(ctx, 42)
			return err
		},
		"LeaseTimeToLive": func(ctx context.Context, rt *Transport) error {
			_, err := rt.LeaseTimeToLive(ctx, 42)
			return err
		},
		"LeaseList": func(ctx context.Context, rt *Transport) error {
			_, err := rt.LeaseList(ctx)
			return err
		},
	})
}
