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

package lifecycle_test

import (
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// A state machine keeps revision history exactly when its descriptor
// declares the MVCC capability; an unregistered type keeps none.
func TestStateMachineKeepsRevisionHistory(t *testing.T) {
	statemachine.RegisterWithDescriptor("lifecycle-test-mvcc", func() sm.CreateFunc { return nil },
		statemachine.Descriptor{Capabilities: []statemachine.Capability{statemachine.CapabilityKV, statemachine.CapabilityMVCC}})
	statemachine.RegisterWithDescriptor("lifecycle-test-kv", func() sm.CreateFunc { return nil },
		statemachine.Descriptor{Capabilities: []statemachine.Capability{statemachine.CapabilityKV}})

	if !lifecycle.StateMachineKeepsRevisionHistory("lifecycle-test-mvcc") {
		t.Fatal("an MVCC-capable state machine reported no revision history")
	}
	if lifecycle.StateMachineKeepsRevisionHistory("lifecycle-test-kv") {
		t.Fatal("a KV-only state machine reported revision history")
	}
	if lifecycle.StateMachineKeepsRevisionHistory("memory") {
		t.Fatal("the built-in memory state machine keeps no revision history")
	}
	if lifecycle.StateMachineKeepsRevisionHistory("no-such-type") {
		t.Fatal("an unregistered type reported revision history")
	}
}
