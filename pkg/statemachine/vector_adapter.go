//go:build raft

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

import (
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// VectorStandardAdapter wraps VectorConsensusFSM to make the adapter pattern
// explicit. VectorConsensusFSM already implements sm.StateMachine.
type VectorStandardAdapter struct {
	sm.StateMachine // embeds the VectorConsensusFSM
}

// NewVectorStandardAdapter creates a standard adapter around a VectorConsensusFSM.
func NewVectorStandardAdapter(inner sm.StateMachine) *VectorStandardAdapter {
	return &VectorStandardAdapter{StateMachine: inner}
}

// Compile-time assertion that VectorStandardAdapter implements sm.StateMachine.
var _ sm.StateMachine = (*VectorStandardAdapter)(nil)
