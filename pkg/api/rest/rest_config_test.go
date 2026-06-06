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
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// GET /v1/config/initialized
// ---------------------------------------------------------------------------

func TestConfigHandlers_Initialized_True(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{
				Sealed:        false,
				Strategy:      "passphrase",
				InitializedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithBarrierService(svc))

	req := httptest.NewRequest(http.MethodGet, "/v1/config/initialized", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp configInitializedResponse
	decodeJSON(t, rec, &resp)

	if !resp.Initialized {
		t.Fatal("expected initialized=true when barrier has InitializedAt set")
	}
}

func TestConfigHandlers_Initialized_False_ZeroTime(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return &service.BarrierStatusInfo{
				Sealed:   true,
				Strategy: "passphrase",
			}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithBarrierService(svc))

	req := httptest.NewRequest(http.MethodGet, "/v1/config/initialized", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp configInitializedResponse
	decodeJSON(t, rec, &resp)

	if resp.Initialized {
		t.Fatal("expected initialized=false when InitializedAt is zero")
	}
}

func TestConfigHandlers_Initialized_NilBarrierService(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/v1/config/initialized", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp configInitializedResponse
	decodeJSON(t, rec, &resp)

	if resp.Initialized {
		t.Fatal("expected initialized=false when barrier service is nil")
	}
}

func TestConfigHandlers_Initialized_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		statusFn: func(_ context.Context) (*service.BarrierStatusInfo, error) {
			return nil, errors.New("barrier status failed")
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithBarrierService(svc))

	req := httptest.NewRequest(http.MethodGet, "/v1/config/initialized", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)

	var resp configInitializedResponse
	decodeJSON(t, rec, &resp)

	if resp.Initialized {
		t.Fatal("expected initialized=false when barrier status returns error")
	}
}
