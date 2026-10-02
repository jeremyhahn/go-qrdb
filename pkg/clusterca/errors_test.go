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

package clusterca

import (
	"errors"
	"strings"
	"testing"
)

func TestNilProviderError(t *testing.T) {
	err := &NilProviderError{}
	if !strings.Contains(err.Error(), "nil") {
		t.Fatalf("Error() = %q, want it to mention nil", err.Error())
	}
}

func TestProviderError(t *testing.T) {
	wrapped := errors.New("device unreachable")
	err := &ProviderError{Op: "signer", Err: wrapped}
	msg := err.Error()
	if !strings.Contains(msg, "signer") || !strings.Contains(msg, "device unreachable") {
		t.Fatalf("Error() = %q, want it to contain %q and %q", msg, "signer", "device unreachable")
	}
	if err.Unwrap() != wrapped {
		t.Fatalf("Unwrap() = %v, want %v", err.Unwrap(), wrapped)
	}
}
