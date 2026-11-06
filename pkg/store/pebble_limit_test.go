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

package store

import (
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

func assertPebbleLimitRefusal(t *testing.T, r sm.Result, resource protocol.LimitResource, maxCount uint32) {
	t.Helper()
	got, gotMax, ok := protocol.DecodeLimitRefusal(r.Data)
	if !ok || got != resource || gotMax != maxCount {
		t.Fatalf("result %+v is not a limit refusal of %d at %d", r, resource, maxCount)
	}
}

func assertPebbleAdmitted(t *testing.T, r sm.Result, what string) {
	t.Helper()
	if _, _, ok := protocol.DecodeAppErrorKind(r.Data); ok {
		t.Fatalf("%s was refused: %q", what, r.Data)
	}
}

// Creates proposed through different nodes apply in log order; the cap is
// decided against the index set each applies over (go-qrdb#1088).
func TestPebbleStateMachine_IndexCreate_CapAdmitsTheLastSlotOnce(t *testing.T) {
	s := openExtendedStore(t)
	capped := protocol.IndexCreateOptions{MaxIndexes: 1}
	results := applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeIndexCreateOpts("a_idx", "a", capped))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeIndexCreateOpts("b_idx", "b", capped))},
		{Index: 3, Cmd: mustEncode(protocol.EncodeIndexCreateOpts("a_idx", "a", capped))},
	})
	assertPebbleAdmitted(t, results[0], "the first create")
	assertPebbleLimitRefusal(t, results[1], protocol.LimitIndexes, 1)
	assertPebbleAdmitted(t, results[2], "a create naming the held index")
	if _, ok := s.indexes["b_idx"]; ok {
		t.Fatal("the refused create left an index behind")
	}
}
