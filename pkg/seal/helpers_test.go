// Copyright (c) 2026 Jeremy Hahn
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

package seal

import (
	"bytes"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// testLogger returns a discard logger shared across all test files.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testSoftwareStrategy creates a SoftwareStrategy with a test passphrase.
// Shared across multiple test files.
func testSoftwareStrategy(t *testing.T) *SoftwareStrategy {
	t.Helper()
	s, err := NewSoftwareStrategy([]byte("test-master-password"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	return s
}

// newRecoveryStrategyForTest builds a recovery strategy from a copy of an
// ordinary key slice, leaving the caller's slice untouched.
func newRecoveryStrategyForTest(key []byte, delegate SealingStrategy) (*recoveryStrategy, error) {
	buf, err := secure.NewBufferFromBytes(bytes.Clone(key))
	if err != nil {
		return nil, err
	}
	return newRecoveryStrategyFromBuffer(buf, delegate)
}

func TestItoa(t *testing.T) {
	for in, want := range map[int]string{0: "0", 42: "42", -7: "-7"} {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
}
