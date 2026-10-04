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

//go:build !integration

package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// TestHandleCeremonyInitialize_PerSourceLockoutDoesNotCrossAddresses is the
// go-qrdb#560 review C acceptance test for REST: two requests carrying
// different remote addresses -- exactly what remoteAddrMiddleware records
// from r.RemoteAddr on every real request, reproduced here directly since
// this test drives the handler the same way ceremony_realstack_test.go
// does -- and a wrong SO PIN repeated past auth.SOPINFreeFailures from
// address A locks out ONLY A's principal; address B, presenting the
// identical wrong PIN immediately afterward, still gets the ordinary 401
// invalid-PIN refusal, never the 429 a lockout answers. Before go-qrdb#560
// review C, every unauthenticated REST caller shared ONE anonymous lockout
// bucket, so B would have been locked out too, by a guess it never made.
func TestHandleCeremonyInitialize_PerSourceLockoutDoesNotCrossAddresses(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(wrapped), WithCeremonyCA(ca))

	newReq := func(addr string, body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(body))
		r.RemoteAddr = addr
		return r.WithContext(auth.WithRemoteAddr(r.Context(), addr))
	}

	// Drive address A past the free-failure budget.
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		rec := httptest.NewRecorder()
		s.handleCeremonyInitialize(rec, newReq("10.0.0.1:5555", `{"so_pin":"wrong-pin","admin_generate":true}`))
		assertStatus(t, rec, http.StatusUnauthorized)
	}
	// One more wrong guess from A: now locked out (429), not a plain
	// invalid-PIN refusal (401).
	lockedRec := httptest.NewRecorder()
	s.handleCeremonyInitialize(lockedRec, newReq("10.0.0.1:5555", `{"so_pin":"wrong-pin","admin_generate":true}`))
	assertStatus(t, lockedRec, http.StatusTooManyRequests)

	// Address B, a DIFFERENT source, presents the identical wrong PIN
	// immediately afterward: it must see the ordinary 401, never A's 429.
	bRec := httptest.NewRecorder()
	s.handleCeremonyInitialize(bRec, newReq("192.168.1.9:6666", `{"so_pin":"wrong-pin","admin_generate":true}`))
	assertStatus(t, bRec, http.StatusUnauthorized)

	// And B can still succeed with the correct PIN while A remains locked.
	bOKRec := httptest.NewRecorder()
	s.handleCeremonyInitialize(bOKRec, newReq("192.168.1.9:6666", `{"so_pin":"correct-horse-battery","admin_generate":true}`))
	assertStatus(t, bOKRec, http.StatusOK)
}
