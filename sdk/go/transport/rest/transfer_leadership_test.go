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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newTransferLeadershipServer serves /health and answers
// /membership/transfer-leadership with handle, recording the decoded body.
func newTransferLeadershipServer(t *testing.T, got *transport.TransferLeadershipRequest, handle func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
		case "/membership/transfer-leadership":
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(got); err != nil {
				t.Errorf("decode request: %v", err)
			}
			handle(w)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestTransferLeadership_Success(t *testing.T) {
	var got transport.TransferLeadershipRequest
	srv := newTransferLeadershipServer(t, &got, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	if err := rt.TransferLeadership(context.Background(), 1, 3); err != nil {
		t.Fatalf("TransferLeadership: %v", err)
	}
	if got.ShardID != 1 || got.ReplicaID != 3 {
		t.Fatalf("request = %+v, want shard 1 replica 3", got)
	}
}

func TestTransferLeadership_TimeoutCarriesTheRefusal(t *testing.T) {
	want := &service.LeaderTransferTimeoutError{ShardID: 1, FromReplicaID: 1, ToReplicaID: 3, Timeout: 5 * time.Second}
	reason, md, ok := replicaforward.Refusal(want)
	if !ok {
		t.Fatal("the timeout is not a refusal")
	}
	var got transport.TransferLeadershipRequest
	srv := newTransferLeadershipServer(t, &got, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: want.Error(), Code: reason, Metadata: md})
	})
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.TransferLeadership(context.Background(), 1, 3)
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("err = %v (%T), want *transport.RequestError", err, err)
	}
	if reqErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", reqErr.StatusCode, http.StatusServiceUnavailable)
	}
	if reqErr.Reason != replicaforward.ReasonLeaderTransferTimeout {
		t.Fatalf("reason = %q, want %s", reqErr.Reason, replicaforward.ReasonLeaderTransferTimeout)
	}
	rebuilt, parseErr := replicaforward.ParseLeaderTransferTimeout(reqErr.Metadata)
	if parseErr != nil {
		t.Fatalf("ParseLeaderTransferTimeout: %v", parseErr)
	}
	if *rebuilt != *want {
		t.Fatalf("rebuilt %+v, want %+v", *rebuilt, *want)
	}
}

func TestTransferLeadership_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.TransferLeadership(context.Background(), 1, 3)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("err = %v (%T), want *transport.ClosedError", err, err)
	}
}
