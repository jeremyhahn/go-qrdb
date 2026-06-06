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

package mcp

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// jsonEncodeError
// ---------------------------------------------------------------------------

func TestJsonEncodeError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("boom")
	e := &jsonEncodeError{Err: inner}
	if !strings.Contains(e.Error(), "JSON encode failed") {
		t.Errorf("expected 'JSON encode failed', got %q", e.Error())
	}
	if !errors.Is(e, inner) {
		t.Error("Unwrap chain broken")
	}
}

// ---------------------------------------------------------------------------
// nilRequestError
// ---------------------------------------------------------------------------

func TestNilRequestError_Error(t *testing.T) {
	e := &nilRequestError{Method: "Test"}
	if !strings.Contains(e.Error(), "nil request") {
		t.Errorf("expected 'nil request', got %q", e.Error())
	}
	if !strings.Contains(e.Error(), "Test") {
		t.Errorf("expected method name, got %q", e.Error())
	}
}
