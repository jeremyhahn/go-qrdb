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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// malformedPINArgs are initialize requests whose PINs are not JSON strings.
// Before the argument check, mcp.ParseString read every one of them as an
// absent PIN, pin.Provision provisioned nothing, and initialization went on.
func malformedPINArgs() map[string]map[string]any {
	return map[string]map[string]any{
		"both b64 arrays":   {"so_pin_b64": []any{"MTIzNA=="}, "user_pin_b64": []any{"NTY3OA=="}},
		"both b64 objects":  {"so_pin_b64": map[string]any{}, "user_pin_b64": map[string]any{}},
		"so_pin_b64 number": {"so_pin_b64": float64(1234), "user_pin": "5678"},
		"user_pin bool":     {"so_pin": "1234", "user_pin": true},
	}
}

// assertNothingProvisioned fails if the PIN backend saw any write.
func assertNothingProvisioned(t *testing.T, backend *stubPINBackend) {
	t.Helper()
	if backend.setSOPINCalls != 0 || backend.setUserPINCalls != 0 {
		t.Fatalf("PIN backend reached: SetSOPIN x%d, SetUserPIN x%d; want none",
			backend.setSOPINCalls, backend.setUserPINCalls)
	}
}

func TestMCP_HandleBarrierInitialize_MalformedPINsNeverProvision(t *testing.T) {
	for name, args := range malformedPINArgs() {
		t.Run(name, func(t *testing.T) {
			initialized := false
			svc := newMockBarrierService()
			svc.initializeFn = func(context.Context, []byte) error {
				initialized = true
				return nil
			}
			backend := newStubPINBackend()
			args["secret"] = "s"
			_, err := handleBarrierInitialize(svc, backend)(context.Background(),
				buildCallToolRequest(t, "qrdb_barrier_initialize", args))
			var argErr *ToolArgumentError
			if !errors.As(err, &argErr) {
				t.Fatalf("err = %v, want a ToolError over *ToolArgumentError", err)
			}
			if initialized {
				t.Fatal("barrier initialize was called with a malformed PIN argument")
			}
			assertNothingProvisioned(t, backend)
		})
	}
}

func TestMCP_HandleBarrierShamirInitialize_MalformedPINsNeverProvision(t *testing.T) {
	for name, args := range malformedPINArgs() {
		t.Run(name, func(t *testing.T) {
			initialized := false
			svc := newMockBarrierService()
			svc.initializeShamirFn = func(context.Context, int, int, seal.FactorPolicy) (*service.ShamirInitInfo, error) {
				initialized = true
				return &service.ShamirInitInfo{}, nil
			}
			backend := newStubPINBackend()
			args["threshold"] = float64(2)
			args["total_shares"] = float64(3)
			_, err := handleBarrierShamirInitialize(svc, backend)(context.Background(),
				buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", args))
			var argErr *ToolArgumentError
			if !errors.As(err, &argErr) {
				t.Fatalf("err = %v, want a ToolError over *ToolArgumentError", err)
			}
			if initialized {
				t.Fatal("Shamir initialize was called with a malformed PIN argument")
			}
			assertNothingProvisioned(t, backend)
		})
	}
}

// TestMCP_HandleBarrierInitialize_WellFormedPINsProvision is the control: the
// same doubles do record a provisioning and an initialization when the PINs
// are strings, so the assertions above are not vacuous.
func TestMCP_HandleBarrierInitialize_WellFormedPINsProvision(t *testing.T) {
	initialized := false
	svc := newMockBarrierService()
	svc.initializeFn = func(context.Context, []byte) error {
		initialized = true
		return nil
	}
	backend := newStubPINBackend()
	_, err := handleBarrierInitialize(svc, backend)(context.Background(),
		buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{
			"secret": "s", "so_pin_b64": "MTIzNA==", "user_pin_b64": "NTY3OA==",
		}))
	if err != nil {
		t.Fatalf("handleBarrierInitialize: %v", err)
	}
	if !initialized || backend.setSOPINCalls != 1 || backend.setUserPINCalls != 1 {
		t.Fatalf("initialized=%v SetSOPIN x%d SetUserPIN x%d; want true, 1, 1",
			initialized, backend.setSOPINCalls, backend.setUserPINCalls)
	}
	if backend.storedSOPIN != "1234" || backend.storedUserPIN != "5678" {
		t.Fatalf("stored PINs = %q, %q; want 1234, 5678", backend.storedSOPIN, backend.storedUserPIN)
	}
}
