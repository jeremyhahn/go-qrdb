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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
)

// A member add refused because the node does not advertise a feature the
// group uses is a conflict the operator resolves by upgrading the node --
// 409 naming the node and the feature, not a 500.
func TestHandler_MembershipAdd_RefusedReplicaIsConflict(t *testing.T) {
	refusal := &featuregate.UnsupportedReplicaError{ShardID: 1, Address: "10.0.0.3:63001", Feature: featuregate.GuardedWrites}
	adminSvc := &mockAdminService{
		addMemberFn: func(context.Context, uint64, uint64, string) error { return refusal },
	}
	srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0")

	body := `{"shard_id":1,"replica_id":3,"address":"10.0.0.3:63001"}`
	req := httptest.NewRequest(http.MethodPost, "/membership/add", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "guarded-writes") {
		t.Fatalf("body %q does not name the missing feature", rec.Body.String())
	}
	if got := mapHTTPError(refusal); got != http.StatusConflict {
		t.Fatalf("mapHTTPError = %d, want %d", got, http.StatusConflict)
	}
}
