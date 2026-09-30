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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
)

// #231: five handlers called io.ReadAll(r.Body) with no bound. The HTTP read
// timeout bounds DURATION, not bytes, so a fast authenticated caller could hand
// any of them a body of arbitrary size and the server allocated all of it before
// the service layer saw anything.
//
// These tests exercise readBoundedBody directly rather than through a routed
// request. The helper IS the bound -- every one of the five now calls it and
// nothing else -- and driving it directly is what lets the AT-THE-LIMIT case be
// asserted: routing a 16 MiB PUT through a live server to prove a memory guard
// would allocate the production bound to test it, which is the shape
// decodeGuardedRequest's own parameters were introduced to avoid.

// bodyBoundResult runs readBoundedBody over n bytes at the given limit.
func bodyBoundResult(t *testing.T, n int, limit int64) (status int, body []byte, ok bool) {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/kv/k", strings.NewReader(strings.Repeat("a", n)))
	body, ok = readBoundedBody(rec, r, limit)
	return rec.Code, body, ok
}

// BOTH SIDES, per the issue: a rejection test alone does not prove a too-tight
// bound has not broken legitimate large requests.
func TestReadBoundedBody_AdmitsAtTheLimitAndRefusesOver(t *testing.T) {
	const limit = int64(1024)

	t.Run("one byte under is admitted", func(t *testing.T) {
		_, body, ok := bodyBoundResult(t, int(limit)-1, limit)
		if !ok {
			t.Fatal("a body under the limit was refused")
		}
		if len(body) != int(limit)-1 {
			t.Errorf("read %d bytes, want %d", len(body), limit-1)
		}
	})

	t.Run("exactly at the limit is admitted", func(t *testing.T) {
		_, body, ok := bodyBoundResult(t, int(limit), limit)
		if !ok {
			t.Fatal("a body exactly at the limit was refused; the bound is off by one")
		}
		if len(body) != int(limit) {
			t.Errorf("read %d bytes, want %d", len(body), limit)
		}
	})

	t.Run("one byte over is refused with 413", func(t *testing.T) {
		status, body, ok := bodyBoundResult(t, int(limit)+1, limit)
		if ok {
			t.Fatal("a body over the limit was admitted")
		}
		if body != nil {
			t.Errorf("refused body returned %d bytes; nothing should be handed back", len(body))
		}
		if status != http.StatusRequestEntityTooLarge {
			t.Errorf("status %d, want 413 -- the caller can act on this by sending less", status)
		}
	})
}

// The bound is reported, not merely applied. A caller that cannot see the limit
// cannot choose a size that will be accepted, which is why RequestTooLargeError
// carries it.
func TestReadBoundedBody_ReportsTheBound(t *testing.T) {
	const limit = int64(2048)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/kv/k", strings.NewReader(strings.Repeat("a", int(limit)+1)))

	if _, ok := readBoundedBody(rec, r, limit); ok {
		t.Fatal("over-limit body admitted")
	}
	if got := rec.Body.String(); !strings.Contains(got, "2048") {
		t.Errorf("the 413 body does not name the limit, so a caller cannot choose a legal size: %s", got)
	}
}

// A genuine read failure is a 400, not a 413: the caller cannot fix it by
// sending less, and conflating the two would tell them to do something useless.
func TestReadBoundedBody_ReadFailureIsNotTooLarge(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/kv/k", errReader{})

	if _, ok := readBoundedBody(rec, r, 1024); ok {
		t.Fatal("a failing body was admitted")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400 for a read failure", rec.Code)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// The two production bounds, asserted against what they are derived from rather
// than against numbers this file also wrote.
func TestBodyBoundsAreDerivedFromTheEntryCeiling(t *testing.T) {
	if maxKVPutRequestBytes != int64(guardedEntryCeiling) {
		t.Errorf("maxKVPutRequestBytes = %d, want the entry ceiling %d: a PUT body becomes "+
			"exactly one Raft entry, so a larger one cannot be proposed whatever this layer accepts",
			maxKVPutRequestBytes, guardedEntryCeiling)
	}
	// The control-plane bound is a policy figure, not a derived one, and the
	// only property asserted is the one that matters: it is far above any legal
	// body and far below an unbounded allocation.
	if maxControlPlaneRequestBytes >= int64(guardedEntryCeiling) {
		t.Errorf("maxControlPlaneRequestBytes = %d is not below the entry ceiling %d; "+
			"a few scalar fields should not be allowed an entry's worth of bytes",
			maxControlPlaneRequestBytes, guardedEntryCeiling)
	}
}

// TestRebalancePlanBoundAdmitsAPlanTheServerCanProduce is the regression test
// for the defect the review caught: /v1/rebalance/execute was first given the
// small control-plane cap, but its body is the whole plan /v1/rebalance/plan
// returned, and the planner is unbounded when max_moves is 0. A cap below what
// the planner can emit lets the server generate a plan it then refuses.
func TestRebalancePlanBoundAdmitsAPlanTheServerCanProduce(t *testing.T) {
	// A plan at the move ceiling, encoded exactly as the execute endpoint
	// receives it -- through the same wire type, not a hand-built string.
	plan := rebalancePlanResponse{
		Moves:               make([]rebalanceMoveResponse, rebalancePlanMoveCeiling),
		EstimatedDurationMs: 1 << 40,
		CurrentImbalance:    0.123456789,
	}
	for i := range plan.Moves {
		plan.Moves[i] = rebalanceMoveResponse{
			ShardID:        ^uint64(0),
			FromNode:       strings.Repeat("n", 64),
			ToNode:         strings.Repeat("m", 64),
			EstimatedBytes: ^uint64(0),
		}
	}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if int64(len(body)) > maxRebalancePlanRequestBytes {
		t.Fatalf("a plan of %d moves encodes to %d bytes, over the %d-byte bound -- "+
			"REST would refuse a plan it can itself produce",
			rebalancePlanMoveCeiling, len(body), maxRebalancePlanRequestBytes)
	}

	// And the bound must be far above the scalar control-plane cap, which is
	// what it was wrongly given.
	if maxRebalancePlanRequestBytes <= maxControlPlaneRequestBytes {
		t.Errorf("the rebalance bound %d is not above the control-plane cap %d; "+
			"a plan is data proportional to the cluster, not a scalar document",
			maxRebalancePlanRequestBytes, maxControlPlaneRequestBytes)
	}
}

var _ io.Reader = errReader{}

// TestForwardedBodiesFitTheOnBehalfBound: a forwarded write carries an
// on-behalf assertion, and the leader reads its body under
// forward.MaxBodyBytes before it can check the assertion's body binding. Every
// body a handler forwards must fit, or the leader refuses a write the
// follower accepted.
func TestForwardedBodiesFitTheOnBehalfBound(t *testing.T) {
	for name, bound := range map[string]int64{
		"kv put":            maxKVPutRequestBytes,
		"guarded write":     maxGuardedRequestBytes,
		"control plane":     maxControlPlaneRequestBytes,
		"rebalance execute": maxRebalancePlanRequestBytes,
	} {
		if bound > forward.MaxBodyBytes {
			t.Errorf("%s bound %d exceeds forward.MaxBodyBytes %d: the leader would refuse the forwarded body",
				name, bound, forward.MaxBodyBytes)
		}
	}
}
