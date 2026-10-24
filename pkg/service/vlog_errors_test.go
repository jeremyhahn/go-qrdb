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
	"strings"
	"testing"
)

func TestLogFeatureNotActiveError_NamesTenantAndShard(t *testing.T) {
	msg := (&LogFeatureNotActiveError{TenantID: "acme", ShardID: 7}).Error()
	for _, want := range []string{`"acme"`, "shard 7", "verifiable-log"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
}

func TestLogFeatureNotActiveError_SystemTenant(t *testing.T) {
	msg := (&LogFeatureNotActiveError{}).Error()
	if !strings.Contains(msg, `""`) || !strings.Contains(msg, "shard 0") {
		t.Errorf("Error() = %q, want the empty tenant and shard 0 named", msg)
	}
}
