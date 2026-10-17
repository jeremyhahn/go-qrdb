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

package errorinfo

import (
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func withDetails(t *testing.T, details ...*errdetails.ErrorInfo) *status.Status {
	t.Helper()
	st := status.New(codes.Unavailable, "refused")
	if len(details) == 0 {
		return st
	}
	// A RetryInfo ahead of the ErrorInfo proves Refusal skips detail types it
	// does not read rather than stopping at the first.
	withRetry, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(time.Second)})
	if err != nil {
		t.Fatalf("WithDetails(RetryInfo): %v", err)
	}
	for _, d := range details {
		if withRetry, err = withRetry.WithDetails(d); err != nil {
			t.Fatalf("WithDetails(ErrorInfo): %v", err)
		}
	}
	return withRetry
}

func TestRefusal_ReturnsTheDomainsReasonAndMetadata(t *testing.T) {
	st := withDetails(t,
		&errdetails.ErrorInfo{Domain: "other.v1", Reason: "OTHER"},
		&errdetails.ErrorInfo{
			Domain:   "qrdb.v1",
			Reason:   "LEADER_TRANSFER_TIMEOUT",
			Metadata: map[string]string{"shard_id": "7"},
		},
	)

	reason, md := Refusal(st, "qrdb.v1")
	if reason != "LEADER_TRANSFER_TIMEOUT" {
		t.Fatalf("reason = %q, want LEADER_TRANSFER_TIMEOUT", reason)
	}
	if md["shard_id"] != "7" {
		t.Fatalf("metadata = %v, want shard_id=7", md)
	}
}

func TestRefusal_EmptyWithoutAMatchingDetail(t *testing.T) {
	for name, st := range map[string]*status.Status{
		"no details":   withDetails(t),
		"other domain": withDetails(t, &errdetails.ErrorInfo{Domain: "other.v1", Reason: "OTHER"}),
	} {
		t.Run(name, func(t *testing.T) {
			reason, md := Refusal(st, "qrdb.v1")
			if reason != "" || md != nil {
				t.Fatalf("Refusal = (%q, %v), want empty", reason, md)
			}
		})
	}
}

// TestRefusalDomain_MatchesServer pins the domain the gRPC and Unix
// transports pass to Refusal to the one the server sends its refusals in.
func TestRefusalDomain_MatchesServer(t *testing.T) {
	if transport.RefusalDomain != replicaforward.ErrorDomain {
		t.Fatalf("transport.RefusalDomain = %q, server sends %q", transport.RefusalDomain, replicaforward.ErrorDomain)
	}
}
