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

package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestRebalanceSkippedMove_JSONCarriesReasonNotErr: a transport that encodes
// the plan whole (MCP) carries the refusal's text in Reason and never the
// error value itself.
func TestRebalanceSkippedMove_JSONCarriesReasonNotErr(t *testing.T) {
	refusal := errors.New("destination is outside the tenant serving set")
	plan := RebalancePlanResult{Skipped: []RebalanceSkippedMove{{
		ShardID: 7, FromNode: "a", ToNode: "b", Reason: refusal.Error(), Err: refusal,
	}}}
	out, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded struct {
		Skipped []map[string]any
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(decoded.Skipped) != 1 {
		t.Fatalf("skipped = %s, want one entry", out)
	}
	got := decoded.Skipped[0]
	if _, ok := got["Err"]; ok {
		t.Fatalf("skipped entry carries Err on the wire: %s", out)
	}
	if reason, _ := got["Reason"].(string); !strings.Contains(reason, "serving set") {
		t.Fatalf("skipped Reason = %v, want the refusal text", got["Reason"])
	}
}
