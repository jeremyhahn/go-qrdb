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

package lifecycle

import (
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// A state machine keeps revision history exactly when its descriptor
// declares the MVCC capability; an unregistered type keeps none.
//
// The two synthetic types go into a test-local registry, never the
// process-global one: smCells requires the global registry to hold exactly
// the production types its battery covers, and a type registered here would
// outlive this test and fail it on every -count iteration after the first.
// The test is in package lifecycle so it reaches the unexported seam
// directly: an export_test.go read from lifecycle_test is undefined to gosec's
// loader and reddens the Security job (see GOSEC_FLAGS in the Makefile).
func TestStateMachineKeepsRevisionHistory(t *testing.T) {
	r := statemachine.NewRegistry()
	r.RegisterWithDescriptor("lifecycle-test-mvcc", func() sm.CreateFunc { return nil },
		statemachine.Descriptor{Capabilities: []statemachine.Capability{statemachine.CapabilityKV, statemachine.CapabilityMVCC}})
	r.RegisterWithDescriptor("lifecycle-test-kv", func() sm.CreateFunc { return nil },
		statemachine.Descriptor{Capabilities: []statemachine.Capability{statemachine.CapabilityKV}})

	if !keepsRevisionHistory(r.Describe, "lifecycle-test-mvcc") {
		t.Fatal("an MVCC-capable state machine reported no revision history")
	}
	if keepsRevisionHistory(r.Describe, "lifecycle-test-kv") {
		t.Fatal("a KV-only state machine reported revision history")
	}
	if StateMachineKeepsRevisionHistory("lifecycle-test-mvcc") {
		t.Fatal("a type registered only in a test-local registry leaked into the global one")
	}
	if StateMachineKeepsRevisionHistory("memory") {
		t.Fatal("the built-in memory state machine keeps no revision history")
	}
	if StateMachineKeepsRevisionHistory("no-such-type") {
		t.Fatal("an unregistered type reported revision history")
	}
}
