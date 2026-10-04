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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// decodeErrorBody decodes a REST error body.
func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) errorResponse {
	t.Helper()
	var body errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body
}

// TestWriteErrorJSON_NotAReplicaCarriesTheReplicaSet: the refusal's replica
// set travels in the error body as the same metadata every transport carries.
func TestWriteErrorJSON_NotAReplicaCarriesTheReplicaSet(t *testing.T) {
	refusal := &placement.NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 42, NodeID: "node-3", LeaderReplicaID: 1,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "node-1", RaftAddr: "192.0.2.1:7000"}}}
	rec := httptest.NewRecorder()
	writeErrorJSON(rec, http.StatusServiceUnavailable, &service.ReadError{Err: refusal})

	body := decodeErrorBody(t, rec)
	if body.Code != replicaforward.ReasonNotAReplica || body.Error == "" {
		t.Fatalf("body = %+v; want code %s and the message", body, replicaforward.ReasonNotAReplica)
	}
	if got, err := replicaforward.ParseNotAReplica(body.Metadata); err != nil || !reflect.DeepEqual(got, refusal) {
		t.Fatalf("metadata decodes to %+v, %v; want %+v", got, err, refusal)
	}
}

// TestWriteErrorJSON_NotLeaderNamesTheLeader and nothing else is touched.
func TestWriteErrorJSON_NotLeaderNamesTheLeader(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErrorJSON(rec, http.StatusServiceUnavailable, &service.NotLeaderError{ShardID: 4, LeaderAddress: "192.0.2.2:7000"})
	if body := decodeErrorBody(t, rec); body.Code != replicaforward.ReasonNotLeader || replicaforward.LeaderRaftAddr(body.Metadata) != "192.0.2.2:7000" {
		t.Fatalf("body = %+v; want NOT_LEADER naming the leader", body)
	}

	rec = httptest.NewRecorder()
	writeErrorJSON(rec, http.StatusInternalServerError, errors.New("boom"))
	if rec.Body.String() != "{\"error\":\"boom\"}\n" {
		t.Fatalf("an unrelated error body = %q; want it unchanged", rec.Body.String())
	}
}
