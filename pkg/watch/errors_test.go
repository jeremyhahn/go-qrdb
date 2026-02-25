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
	"errors"
	"testing"
)

func TestHubClosedError_Error_ReturnsExpectedMessage(t *testing.T) {
	err := &HubClosedError{}
	want := "watch hub is closed"
	if got := err.Error(); got != want {
		t.Fatalf("HubClosedError.Error() = %q, want %q", got, want)
	}
}

func TestHubClosedError_ImplementsErrorInterface(t *testing.T) {
	var err error = &HubClosedError{}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestHubClosedError_ErrorsAs(t *testing.T) {
	err := &HubClosedError{}
	var target *HubClosedError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *HubClosedError")
	}
}

func TestHubClosedError_NotNil(t *testing.T) {
	err := &HubClosedError{}
	if err == nil {
		t.Fatal("expected non-nil error")
	}
}
