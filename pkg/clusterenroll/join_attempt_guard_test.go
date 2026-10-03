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

package clusterenroll

import "testing"

func TestJoinAttemptGuard_AllowsWithinBudget(t *testing.T) {
	g := newJoinAttemptGuard()
	defer func() { _ = g.close() }()

	if !g.allow("127.0.0.1") {
		t.Fatal("first attempt from a fresh source must be allowed")
	}
}

func TestJoinAttemptGuard_RateLimitsBurst(t *testing.T) {
	g := newJoinAttemptGuard()
	defer func() { _ = g.close() }()

	allowedCount := 0
	for i := 0; i < joinAttemptRatePolicy.Burst+5; i++ {
		if g.allow("203.0.113.5") {
			allowedCount++
		}
	}
	if allowedCount > joinAttemptRatePolicy.Burst {
		t.Fatalf("allowed %d attempts in a burst, want at most Burst (%d)", allowedCount, joinAttemptRatePolicy.Burst)
	}
	if allowedCount == 0 {
		t.Fatal("a fresh source's first attempt must be allowed")
	}
}

func TestJoinAttemptGuard_LockoutAfterThreshold(t *testing.T) {
	g := newJoinAttemptGuard()
	defer func() { _ = g.close() }()

	const source = "198.51.100.9"
	for i := 0; i < joinLockoutThreshold; i++ {
		g.recordFailure(source)
	}
	if g.allow(source) {
		t.Fatal("source must be locked out after joinLockoutThreshold consecutive failures")
	}
}

func TestJoinAttemptGuard_SuccessClearsFailures(t *testing.T) {
	g := newJoinAttemptGuard()
	defer func() { _ = g.close() }()

	const source = "198.51.100.10"
	for i := 0; i < joinLockoutThreshold-1; i++ {
		g.recordFailure(source)
	}
	g.recordSuccess(source)
	g.mu.Lock()
	state := g.failures[source]
	g.mu.Unlock()
	if state != nil {
		t.Fatalf("recordSuccess must clear the failure entry, got %+v", state)
	}
}

func TestJoinAttemptGuard_SuccessOnUnknownSourceIsNoOp(t *testing.T) {
	g := newJoinAttemptGuard()
	defer func() { _ = g.close() }()

	// A source with no recorded failures has no entry at all; clearing it
	// must not panic or create one.
	g.recordSuccess("203.0.113.99")
	g.mu.Lock()
	_, ok := g.failures["203.0.113.99"]
	g.mu.Unlock()
	if ok {
		t.Fatal("recordSuccess must not create an entry for a source with no failures")
	}
}

func TestJoinAttemptGuard_PackageLevelFunctions(t *testing.T) {
	restore := ResetJoinAttemptGuardForTest()
	defer restore()

	const source = "192.0.2.77"
	if !AllowJoinAttempt(source) {
		t.Fatal("AllowJoinAttempt must allow a fresh source")
	}
	RecordJoinAttemptFailure(source)
	RecordJoinAttemptSuccess(source)
}

func TestSourceAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"host:port", "127.0.0.1:12345", "127.0.0.1"},
		{"malformed", "not-a-host-port", "not-a-host-port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sourceAddr(fakeAddrForTest(tt.addr))
			if got != tt.want {
				t.Errorf("sourceAddr(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestSourceAddr_NilAddr(t *testing.T) {
	if got := sourceAddr(nil); got != "" {
		t.Errorf("sourceAddr(nil) = %q, want empty string", got)
	}
}

// fakeAddrForTest implements net.Addr over a literal string, for exercising
// sourceAddr's SplitHostPort success and failure branches directly.
type fakeAddrForTest string

func (f fakeAddrForTest) Network() string { return "test" }
func (f fakeAddrForTest) String() string  { return string(f) }
