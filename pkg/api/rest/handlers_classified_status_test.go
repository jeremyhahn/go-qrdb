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

//go:build !integration

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestAdminAndRebalanceHandlers_RenderTheErrorsClass pins that the admin and
// rebalance handlers answer a service error with its class's status rather
// than a hardcoded 500 (go-qrdb#557). Each handler is driven with an error the
// classification table knows — a caller deadline — which must reach the
// client as 504, not as an internal fault.
func TestAdminAndRebalanceHandlers_RenderTheErrorsClass(t *testing.T) {
	classified := context.DeadlineExceeded
	want := http.StatusGatewayTimeout

	admin := &mockAdminService{
		statusFn:       func(context.Context) (*service.StatusInfo, error) { return nil, classified },
		peersFn:        func(context.Context) (*service.PeersInfo, error) { return nil, classified },
		capabilitiesFn: func(context.Context) (*capabilities.Capabilities, error) { return nil, classified },
		startShardFn:   func(context.Context, *service.StartShardRequest) error { return classified },
		stopShardFn:    func(context.Context, uint64) error { return classified },
		getShardStateFn: func(context.Context, uint64) (*service.ShardStateResponse, error) {
			return nil, classified
		},
		getTopologyFn: func(context.Context) (*service.TopologyResponse, error) { return nil, classified },
	}
	rb := newStubRebalanceService()
	rb.statsFn = func(context.Context, uint64) (*service.ShardStats, error) { return nil, classified }
	rb.planFn = func(context.Context, *service.RebalancePlanOptions) (*service.RebalancePlanResult, error) {
		return nil, classified
	}
	rb.reportFn = func(context.Context) (*service.ImbalanceReportResult, error) { return nil, classified }
	srv := NewServer(&mockKVService{}, admin, ":0", WithRebalanceService(rb))

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		serve  func(http.ResponseWriter, *http.Request)
	}{
		{"status", http.MethodGet, "/status", "", srv.handleStatus},
		{"peers", http.MethodGet, "/peers", "", srv.handlePeers},
		{"capabilities", http.MethodGet, "/capabilities", "", srv.handleCapabilities},
		{"start shard", http.MethodPost, "/admin/shards", `{"shard_id":1}`, srv.handleStartShard},
		{"topology", http.MethodGet, "/admin/topology", "", srv.handleGetTopology},
		{"stop shard", http.MethodDelete, "/admin/shards/1", "", func(w http.ResponseWriter, r *http.Request) {
			srv.handleStopShard(w, r, "1")
		}},
		{"shard state", http.MethodGet, "/admin/shards/1", "", func(w http.ResponseWriter, r *http.Request) {
			srv.handleGetShardState(w, r, "1")
		}},
		{"rebalance shard stats", http.MethodGet, "/v1/rebalance/shards/1", "", srv.handleRebalanceShardStats},
		{"rebalance plan", http.MethodPost, "/v1/rebalance/plan", `{}`, srv.handleRebalancePlan},
		{"imbalance report", http.MethodGet, "/v1/rebalance/report", "", srv.handleImbalanceReport},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			tt.serve(rec, req)
			if rec.Code != want {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, want, rec.Body.String())
			}
		})
	}
}

// TestAdminAndRebalanceHandlers_UnclassifiedErrorIsInternal is the other half:
// an error the table does not recognise still answers 500, so routing through
// the mapper did not soften a genuine fault.
func TestAdminAndRebalanceHandlers_UnclassifiedErrorIsInternal(t *testing.T) {
	rb := newStubRebalanceService()
	rb.reportFn = func(context.Context) (*service.ImbalanceReportResult, error) {
		return nil, &unclassifiedTestError{}
	}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, ":0", WithRebalanceService(rb))

	rec := httptest.NewRecorder()
	srv.handleImbalanceReport(rec, httptest.NewRequest(http.MethodGet, "/v1/rebalance/report", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// unclassifiedTestError is an error no classification rule recognises.
type unclassifiedTestError struct{}

func (*unclassifiedTestError) Error() string { return "unclassified" }
