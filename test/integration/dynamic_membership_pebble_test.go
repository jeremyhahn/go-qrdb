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

//go:build integration

package integration_test

import (
	"net/http"
	"testing"
	"time"
)

// Dynamic membership registers each node with a lease-attached put. The
// default pebble state machine applies that put but keeps no revision
// history to read it back at, which used to fail the registration after it
// had applied -- and with it the node's startup. Every node of a pebble
// cluster must now register and stay up.
func TestCluster_DynamicMembershipOnPebble(t *testing.T) {
	adminPorts, _, ca, logs := startThreeNodeClusterLogged(t, "pebble", "--dynamic-membership")

	deadline := time.Now().Add(30 * time.Second)
	for i, l := range logs {
		for !l.Contains("registered cluster member") {
			if l.Contains("failed to register self") {
				t.Fatalf("node %d failed to register its membership on the pebble state machine", i+1)
			}
			if time.Now().After(deadline) {
				t.Fatalf("node %d never registered its membership", i+1)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	for i, l := range logs {
		if l.Contains("failed to register self") {
			t.Fatalf("node %d failed to register its membership on the pebble state machine", i+1)
		}
		if code := httpHealth(t, ca.Pool, adminPorts[i]); code != http.StatusOK {
			t.Fatalf("node %d: health=%d after registering, want 200", i+1, code)
		}
	}
}
