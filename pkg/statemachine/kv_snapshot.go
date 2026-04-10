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

import "io"

// SaveSnapshot delegates snapshot serialization to the backend.
func (k *KVStateMachine) SaveSnapshot(w io.Writer, stopper <-chan struct{}) error {
	return k.backend.SaveSnapshot(w, stopper)
}

// RecoverSnapshot delegates snapshot recovery to the backend.
func (k *KVStateMachine) RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error {
	return k.backend.RecoverSnapshot(r, stopper)
}

// GetHash delegates hash computation to the backend.
func (k *KVStateMachine) GetHash() (uint64, error) {
	return k.backend.GetHash()
}

// Close delegates resource cleanup to the backend.
func (k *KVStateMachine) Close() error {
	return k.backend.Close()
}
