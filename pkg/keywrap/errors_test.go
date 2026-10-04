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

package keywrap

import (
	"errors"
	"strings"
	"testing"
)

func TestKeyAccessError_ErrorCarriesNoDetail(t *testing.T) {
	err := &KeyAccessError{}
	msg := err.Error()
	if msg == "" {
		t.Fatal("Error() is empty")
	}
	// The whole point of this type: no path, OS error, or length ever
	// renders into its message (go-qrdb review #B4).
	for _, leak := range []string{"/etc/", "/tmp/", ".key", "no such file"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("Error() = %q, must not contain %q", msg, leak)
		}
	}
}

func TestKeyDirectoryError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("permission denied")
	err := &KeyDirectoryError{Dir: "/etc/qrdb/keys", Err: inner}
	if !strings.Contains(err.Error(), "/etc/qrdb/keys") {
		t.Fatalf("Error() = %q, want it to mention the directory", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is(err, inner) = false, want true via Unwrap")
	}
}

func TestProviderRegisterError_Error(t *testing.T) {
	err := &ProviderRegisterError{Reason: "nil factory"}
	if !strings.Contains(err.Error(), "nil factory") {
		t.Fatalf("Error() = %q, want it to mention the reason", err.Error())
	}
}

func TestProviderAlreadyRegisteredError_Error(t *testing.T) {
	err := &ProviderAlreadyRegisteredError{ID: "software"}
	if !strings.Contains(err.Error(), "software") {
		t.Fatalf("Error() = %q, want it to mention the id", err.Error())
	}
}

func TestProviderNotRegisteredError_Error(t *testing.T) {
	err := &ProviderNotRegisteredError{ID: "aws-kms", Available: []string{"software"}}
	msg := err.Error()
	if !strings.Contains(msg, "aws-kms") || !strings.Contains(msg, "software") {
		t.Fatalf("Error() = %q, want it to mention both the missing id and the available set", msg)
	}
}

func TestUnwrapFailedError_Error(t *testing.T) {
	err := &UnwrapFailedError{}
	if err.Error() == "" {
		t.Fatal("Error() is empty")
	}
}

func TestWrapFailedError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("boom")
	err := &WrapFailedError{Err: inner}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Error() = %q, want it to mention the underlying error", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is(err, inner) = false, want true via Unwrap")
	}
}
