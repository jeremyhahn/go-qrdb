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
	"errors"
	"reflect"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestMapServiceError_NotAReplicaCarriesTheReplicaSet: the refusal's replica
// set travels as an ErrorInfo detail a client decodes without parsing the
// message, and the code is unchanged.
func TestMapServiceError_NotAReplicaCarriesTheReplicaSet(t *testing.T) {
	refusal := &placement.NotAReplicaError{
		TenantID: "acme", Database: "orders", ShardID: 42, NodeID: "node-3", LeaderReplicaID: 1,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "node-1", RaftAddr: "192.0.2.1:7000"}},
	}
	for name, err := range map[string]error{
		"bare":    refusal,
		"wrapped": &service.ReadError{Err: refusal},
	} {
		st := status.Convert(mapServiceError(err))
		if st.Code() != codes.Unavailable {
			t.Errorf("%s: code = %v; want Unavailable", name, st.Code())
		}
		reason, md, ok := refusalFromStatus(st)
		if !ok || reason != replicaforward.ReasonNotAReplica {
			t.Fatalf("%s: refusal detail = %q, %v; want %s", name, reason, ok, replicaforward.ReasonNotAReplica)
		}
		got, derr := replicaforward.ParseNotAReplica(md)
		if derr != nil || !reflect.DeepEqual(got, refusal) {
			t.Errorf("%s: decoded %+v, %v; want %+v", name, got, derr, refusal)
		}
	}
}

// TestMapServiceError_NotLeaderNamesTheLeader: a follower's refusal names the
// leader's Raft address, empty when unknown.
func TestMapServiceError_NotLeaderNamesTheLeader(t *testing.T) {
	for addr, err := range map[string]error{
		"192.0.2.2:7000": &service.NotLeaderError{ShardID: 42, LeaderID: 2, LeaderAddress: "192.0.2.2:7000"},
		"":               &service.NotLeaderError{ShardID: 42},
	} {
		reason, md, ok := refusalFromStatus(status.Convert(mapServiceError(err)))
		if !ok || reason != replicaforward.ReasonNotLeader || replicaforward.LeaderRaftAddr(md) != addr {
			t.Errorf("leader %q: refusal detail = %q, %v, %v", addr, reason, md, ok)
		}
	}
}

// TestMapServiceError_OtherErrorsCarryNoRefusal: only the two replica
// refusals carry the detail.
func TestMapServiceError_OtherErrorsCarryNoRefusal(t *testing.T) {
	for _, err := range []error{errors.New("boom"), &service.KeyNotFoundError{Key: "k"}} {
		if reason, _, ok := refusalFromStatus(status.Convert(mapServiceError(err))); ok {
			t.Errorf("%v carried refusal %q", err, reason)
		}
	}
	// A detail from another domain is not qrdb's refusal.
	if _, _, ok := refusalFromStatus(status.New(codes.Unavailable, "x")); ok {
		t.Error("a status with no details decoded as a refusal")
	}
}
