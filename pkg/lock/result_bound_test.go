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

package lock

import (
	"strings"
	"testing"
)

func TestWithMaxResultBytes_SetsTheBound(t *testing.T) {
	var ro RangeOptions
	WithMaxResultBytes(4096)(&ro)
	if ro.MaxResultBytes != 4096 {
		t.Fatalf("MaxResultBytes = %d, want 4096", ro.MaxResultBytes)
	}
	var unset RangeOptions
	if unset.MaxResultBytes != 0 {
		t.Fatalf("zero RangeOptions bounds results at %d, want unbounded", unset.MaxResultBytes)
	}
}

func TestResultTooLargeError_NamesBothFigures(t *testing.T) {
	msg := (&ResultTooLargeError{Limit: 100, Read: 123}).Error()
	if !strings.HasPrefix(msg, "lock: ") || !strings.Contains(msg, "100") || !strings.Contains(msg, "123") {
		t.Fatalf("message %q does not name its origin, the limit and the bytes read", msg)
	}
}
