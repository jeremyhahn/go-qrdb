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

package quic

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Watch is server-streaming
//
// Watch is handled by handleWatchStream on the dedicated streaming path
// (see handleStream + isWatchMethod), NOT the unary request/response dispatch
// table. The full streaming behavior — request decode, watcher creation,
// multi-frame WatchResponse delivery, and client-disconnect cancellation — is
// exercised end-to-end against a real QUIC connection by the multi-protocol
// integration suite (test/integration/api/cli_watch_test.go). These unit
// tests assert the routing invariants.
// ---------------------------------------------------------------------------

func TestIsWatchMethod(t *testing.T) {
	cases := []struct {
		method string
		want   bool
	}{
		{"watch.Watch", true},
		{"watch.WatchPrefix", true},
		{"kv.Get", false},
		{"watch.Other", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isWatchMethod(tc.method); got != tc.want {
			t.Errorf("isWatchMethod(%q) = %v, want %v", tc.method, got, tc.want)
		}
	}
}

// TestWatchMethodsNotInUnaryDispatch verifies the streaming watch methods are
// intentionally absent from the unary handler table — they must be routed to
// handleWatchStream, not invoked as one-shot request/response handlers.
func TestWatchMethodsNotInUnaryDispatch(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithWatch(&mockWatchService{}))
	for _, method := range []string{"watch.Watch", "watch.WatchPrefix"} {
		if _, ok := srv.handlers[method]; ok {
			t.Errorf("method %q must NOT be in the unary dispatch table (it is server-streaming)", method)
		}
	}
}
