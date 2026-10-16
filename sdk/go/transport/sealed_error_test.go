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

package transport

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsBarrierSealed_MatchesThroughWrapping(t *testing.T) {
	sealed := &BarrierSealedError{Message: "barrier sealed"}
	if !IsBarrierSealed(sealed) {
		t.Fatal("bare sealed error not matched")
	}
	if !IsBarrierSealed(fmt.Errorf("probe: %w", sealed)) {
		t.Fatal("wrapped sealed error not matched")
	}
	if got, want := sealed.Error(), "transport: barrier sealed: barrier sealed"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestIsBarrierSealed_RejectsOtherErrors(t *testing.T) {
	for name, err := range map[string]error{
		"nil":     nil,
		"other":   errors.New("unavailable"),
		"request": &RequestError{Method: "Get", Err: errors.New("unavailable")},
	} {
		if IsBarrierSealed(err) {
			t.Errorf("%s: matched as sealed", name)
		}
	}
}
