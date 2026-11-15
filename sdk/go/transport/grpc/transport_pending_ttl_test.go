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

package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A negative TTL is refused before it is cut to whole seconds: "-500ms"
// would otherwise truncate to 0 on the wire and create an account that never
// expires.
func TestGRPCUser_CreateUser_RefusesNegativePendingTTL(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()

	for _, ttl := range []string{"-500ms", "-1ns", "-2s"} {
		t.Run(ttl, func(t *testing.T) {
			u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
				WithoutEmailLoginKey: true, Username: "neg", TenantID: "system",
				Pending: true, PendingTTL: ttl,
			})
			var neg *transport.NegativePendingTTLError
			if !errors.As(err, &neg) {
				t.Fatalf("CreateUser(pending_ttl=%s) = %v, %v; want NegativePendingTTLError", ttl, u, err)
			}
		})
	}
}

// The gRPC wire carries whole seconds, so a positive sub-second TTL would
// truncate to 0 -- no expiry. The client refuses it before sending.
func TestGRPCUser_CreateUser_RefusesSubSecondPendingTTL(t *testing.T) {
	addr, stop := startUserRoleCertServer(t)
	defer stop()
	tr, cleanup := connectUserTransport(t, addr)
	defer cleanup()

	for _, ttl := range []string{"500ms", "1ns", "999ms"} {
		t.Run(ttl, func(t *testing.T) {
			u, err := tr.CreateUser(context.Background(), &transport.CreateUserRequest{
				WithoutEmailLoginKey: true, Username: "sub", TenantID: "system",
				Pending: true, PendingTTL: ttl,
			})
			var sub *transport.SubSecondPendingTTLError
			if !errors.As(err, &sub) {
				t.Fatalf("CreateUser(pending_ttl=%s) = %v, %v; want SubSecondPendingTTLError", ttl, u, err)
			}
		})
	}
}
