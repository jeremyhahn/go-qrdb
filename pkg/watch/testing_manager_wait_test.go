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

package watch

import (
	"testing"
	"time"
)

// deliveryDeadline bounds every wait for an asynchronously delivered watch
// response. It is a failure report, not synchronisation: the tests wait on the
// stream's sent signal, and this only decides how long a genuinely stuck
// delivery is allowed to look like a slow one. It is deliberately far larger
// than any delivery takes, because the 50-100 ms sleeps it replaced were sized
// for an idle host and went red on a busy one (#309).
const deliveryDeadline = 10 * time.Second

// waitResponses blocks until the stream has received at least n responses and
// returns every response it holds, failing the test with the count actually
// observed if the deadline passes first. Send ticks stream.sent, so every wake
// re-reads the slice; one pending tick is enough to guarantee another look.
//
// The loop lives here rather than as a method on the double, because
// testing_manager.go is compiled into the package proper: a method there whose
// only callers are _test.go files is dead code in the non-test build, and
// golangci-lint's unused check says so.
//
// This file carries no build constraint on purpose: it is called from both the
// unconstrained tests and the +integration ones, and a helper's constraint must
// be a superset of every caller's.
func waitResponses(t *testing.T, stream *mockManagerWatchStream, n int) []*ManagerWatchResponse {
	t.Helper()
	deadline := time.After(deliveryDeadline)
	for {
		got := stream.getResponses()
		if len(got) >= n {
			return got
		}
		select {
		case <-stream.sent:
		case <-deadline:
			got = stream.getResponses()
			t.Fatalf("timed out after %v waiting for %d watch responses; stream holds %d", deliveryDeadline, n, len(got))
			return got
		}
	}
}
