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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// TestMCP_CeremonyInitialize_PerSourceLockoutDoesNotCrossAddresses is the
// go-qrdb#560 review C acceptance test for MCP: two contexts carrying
// different remote addresses -- exactly what authHTTPContext records from
// r.RemoteAddr on every real request (now installed unconditionally,
// go-qrdb#560 review C, so this holds even on a node with no AuthMiddleware
// configured), reproduced here directly since this test drives the tool
// handler the same way this package's other ceremony tests do -- and a
// wrong SO PIN repeated past auth.SOPINFreeFailures from address A locks
// out ONLY A's principal; address B, presenting the identical wrong PIN
// immediately afterward, still gets the ordinary pin.ErrPINInvalid
// refusal, never auth.SOPINLockedError. Before go-qrdb#560 review C, every
// unauthenticated MCP caller shared ONE anonymous lockout bucket, so B
// would have been locked out too, by a guess it never made.
func TestMCP_CeremonyInitialize_PerSourceLockoutDoesNotCrossAddresses(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	handler := handleCeremonyInitialize(wrapped, ca)

	ctxFor := func(addr string) context.Context {
		return auth.WithRemoteAddr(context.Background(), addr)
	}

	// Drive address A past the free-failure budget.
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
			"so_pin": "wrong-pin", "admin_generate": true,
		})
		if _, err := handler(ctxFor("10.0.0.1:5555"), req); !errors.Is(err, pin.ErrPINInvalid) {
			t.Fatalf("address A, attempt %d: got %v, want pin.ErrPINInvalid", i+1, err)
		}
	}
	// One more wrong guess from A: now locked out.
	lockedReq := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"so_pin": "wrong-pin", "admin_generate": true,
	})
	_, lockedErr := handler(ctxFor("10.0.0.1:5555"), lockedReq)
	if !errors.Is(lockedErr, pin.ErrPINLocked) {
		t.Fatalf("address A after %d wrong guesses: got %v, want pin.ErrPINLocked", auth.SOPINFreeFailures+1, lockedErr)
	}

	// Address B, a DIFFERENT source, presents the identical wrong PIN
	// immediately afterward: it must see the ordinary invalid-PIN refusal,
	// never A's lockout.
	bReq := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"so_pin": "wrong-pin", "admin_generate": true,
	})
	_, bErr := handler(ctxFor("192.168.1.9:6666"), bReq)
	if !errors.Is(bErr, pin.ErrPINInvalid) {
		t.Fatalf("address B: got %v, want pin.ErrPINInvalid; A's lockout leaked to B", bErr)
	}

	// And B can still succeed with the correct PIN while A remains locked.
	bOKReq := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"so_pin": "correct-horse-battery", "admin_generate": true,
	})
	bOKResult, bOKErr := handler(ctxFor("192.168.1.9:6666"), bOKReq)
	if bOKErr != nil || bOKResult.IsError {
		t.Fatalf("address B with the correct PIN: expected success, got result=%v err=%v", bOKResult, bOKErr)
	}
}
