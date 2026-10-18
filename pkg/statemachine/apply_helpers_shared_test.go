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

package statemachine

// Apply helpers shared by tests under different build constraints: the
// !integration subsystem tests and the unconstrained
// compaction_unsupported_test.go. A helper's constraint must be a superset of
// every caller's, so they live here, unconstrained, rather than in either
// caller's file.

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

func newRegularWithMVCC(ms MVCCStore) *RegularStateMachine {
	return NewRegularStateMachine(WithRegularMVCCStore(ms))
}

func regularApplyCmd(t *testing.T, rsm *RegularStateMachine, cmd []byte) (sm.Result, error) {
	t.Helper()
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := rsm.Update(context.Background(), entries, results)
	return results[0], err
}
