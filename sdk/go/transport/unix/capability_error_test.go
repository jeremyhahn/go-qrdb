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

package unix

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The unix transport is gRPC over a domain socket against the same
// pkg/api/grpc server as the TCP transport, so it receives the same
// codes.Unimplemented that go-qrdb#141 maps a capability refusal to. It has its
// own mapGRPCError, which is exactly how the two drift: the TCP client gained
// the typed error and this one would have kept returning a generic
// RequestError for the identical server-side condition.
func TestMapGRPCError_UnimplementedBecomesACapabilityRefusal(t *testing.T) {
	err := mapGRPCError("Transaction",
		status.Error(codes.Unimplemented, "service: statemachine: mvcc not configured"))

	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("mapGRPCError returned %T (%v); a unix client must be able to detect a "+
			"capability refusal with errors.As exactly as the TCP client can, or graceful "+
			"degradation works on one transport and not its twin", err, err)
	}
	if unsupported.Message != "service: statemachine: mvcc not configured" {
		t.Errorf("message = %q; the server's account of what is missing must survive the "+
			"mapping", unsupported.Message)
	}
}

// The controls. Mapping every status to a capability refusal would satisfy the
// assertion above while telling callers that a missing key, or a server that
// genuinely broke, is an unsupported operation — which stops them retrying
// something that would have succeeded.
func TestMapGRPCError_OtherStatusesAreNotCapabilityRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		code codes.Code
	}{
		{"NotFound stays a key-not-found", codes.NotFound},
		{"Internal stays a request error", codes.Internal},
		{"Unavailable stays a request error", codes.Unavailable},
		{"PermissionDenied stays a request error", codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := mapGRPCError("Get", status.Error(tc.code, "boom"))
			var unsupported *transport.CapabilityUnsupportedError
			if errors.As(err, &unsupported) {
				t.Errorf("%s mapped to a capability refusal", tc.code)
			}
		})
	}
}
