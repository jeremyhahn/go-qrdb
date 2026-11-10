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

package node

import (
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestStartMemberRecord_NoFeatureClient: with no feature client there is
// nothing to publish through, so no advertiser is started and stopping is
// clean. It lives apart from member_record_test.go, whose raft build
// constraint would drop it from every other tag set.
func TestStartMemberRecord_NoFeatureClient(t *testing.T) {
	n := &Node{}
	n.startMemberRecord(nil, qrdbconfig.DefaultConfig())
	if err := n.stopMemberRecord(); err != nil {
		t.Fatalf("stopMemberRecord: %v", err)
	}
	if n.memberAdv.Load() != nil {
		t.Fatal("no advertiser may be published without a feature client")
	}
}
