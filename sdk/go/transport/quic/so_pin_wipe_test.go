// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package quic

import (
	"context"
	"testing"
)

// TestCallWithSecret_WipesThePayload: a QUIC request that carries the SO PIN
// in its message is wiped once the call returns -- here a call that fails,
// the path most likely to forget it.
func TestCallWithSecret_WipesThePayload(t *testing.T) {
	tr := newClosedTransport()
	payload := []byte("so_pin carrying payload")
	_, _ = tr.callWithSecret(context.Background(), "barrier.Rekey", payload)
	for i, b := range payload {
		if b != 0 {
			t.Fatalf("payload byte %d survived the call", i)
		}
	}
}
