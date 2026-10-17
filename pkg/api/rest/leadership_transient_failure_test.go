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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// dataPathFailures are the two verbs' failure writers, each with the wrapper
// its error body keeps.
var dataPathFailures = map[string]struct {
	write   func(http.ResponseWriter, error)
	wrapper string
}{
	"write": {writeProposeFailure, "propose failed"},
	"read":  {writeReadFailure, "read failed"},
}

// A write or read that outlived the leader wait -- the shard had no leader for
// as long as the request could wait -- is refused with 503 NOT_LEADER: nothing
// was applied and the request is retryable once a leader is elected. It is the
// answer every other transport gives, and the only one a forwarding node's
// classifier recognises as a refusal rather than the target's own failure.
func TestDataPathFailure_LeaderUnknownIsServiceUnavailable(t *testing.T) {
	for name, tc := range dataPathFailures {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.write(rec, &service.NotLeaderError{ShardID: 1})

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
			if body := decodeErrorBody(t, rec); body.Code != replicaforward.ReasonNotLeader {
				t.Fatalf("code = %q, want %q", body.Code, replicaforward.ReasonNotLeader)
			}
			rec = httptest.NewRecorder()
			tc.write(rec, &service.NotLeaderError{ShardID: 1})
			if res, ok := classifyRESTAnswer(rec.Result()); !ok || res.Outcome != replicaforward.NotLeader {
				t.Fatalf("classifyRESTAnswer = %+v, %v; want a NotLeader refusal", res, ok)
			}
		})
	}
}

// Raft timing a proposal or read out mid-election is 503 too, as the key-value
// error table says, but not a refusal: a timed-out write may have committed,
// so a forwarding node relays the answer rather than re-sending the request.
// So is a leader stepping down under a write already in its log.
func TestDataPathFailure_RaftTimeoutIsServiceUnavailable(t *testing.T) {
	for name, tc := range map[string]struct {
		write func(http.ResponseWriter, error)
		err   error
	}{
		"write": {writeProposeFailure, &service.ProposeError{Err: quicraft.ErrTimeout}},
		"read":  {writeReadFailure, &service.ReadError{Err: quicraft.ErrTimeout}},
		// A write already in the log when its leader stepped down.
		"maybe-applied write": {writeProposeFailure, &service.ProposeError{
			Err: &quicraft.MaybeAppliedError{Err: &quicraft.NotLeaderError{ShardID: 1}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.write(rec, tc.err)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
			if res, ok := classifyRESTAnswer(rec.Result()); ok {
				t.Fatalf("classifyRESTAnswer = %+v; want the timeout relayed, not taken for a refusal", res)
			}
		})
	}
}

// The wrapper still says which path failed.
func TestDataPathFailure_LeadershipTransientKeepsItsWrapper(t *testing.T) {
	for name, tc := range dataPathFailures {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.write(rec, &service.NotLeaderError{ShardID: 1})
			if body := decodeErrorBody(t, rec); !strings.Contains(body.Error, tc.wrapper) {
				t.Fatalf("error = %q, want it to contain %q", body.Error, tc.wrapper)
			}
		})
	}
}
