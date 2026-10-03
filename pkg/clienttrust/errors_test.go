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

package clienttrust

import (
	"errors"
	"strings"
	"testing"
)

func TestClusterCANotConfiguredError_Error(t *testing.T) {
	err := &ClusterCANotConfiguredError{}
	if !strings.Contains(err.Error(), "no cluster CA configured") {
		t.Errorf("unexpected message: %q", err.Error())
	}
}

func TestClusterCAUnavailableError_Error(t *testing.T) {
	t.Run("without an underlying error", func(t *testing.T) {
		err := &ClusterCAUnavailableError{}
		if !strings.Contains(err.Error(), "cluster CA certificate unavailable") {
			t.Errorf("unexpected message: %q", err.Error())
		}
		if strings.Contains(err.Error(), ":  ") {
			t.Errorf("expected no trailing separator with no underlying error, got %q", err.Error())
		}
	})

	t.Run("with an underlying error", func(t *testing.T) {
		cause := errors.New("barrier sealed")
		err := &ClusterCAUnavailableError{Err: cause}
		if !strings.Contains(err.Error(), "barrier sealed") {
			t.Errorf("expected the underlying cause in the message, got %q", err.Error())
		}
		if !errors.Is(err, cause) {
			t.Errorf("expected errors.Is to reach the underlying cause via Unwrap")
		}
		if got := err.Unwrap(); got != cause {
			t.Errorf("Unwrap() = %v, want %v", got, cause)
		}
	})
}

func TestKeyBUnavailableError_Error(t *testing.T) {
	t.Run("without an underlying error", func(t *testing.T) {
		err := &KeyBUnavailableError{Reason: "sealed"}
		if !strings.Contains(err.Error(), "Key B unavailable: sealed") {
			t.Errorf("unexpected message: %q", err.Error())
		}
		if err.Unwrap() != nil {
			t.Errorf("expected Unwrap() to be nil with no underlying error")
		}
	})

	t.Run("with an underlying error", func(t *testing.T) {
		cause := errors.New("decode failed")
		err := &KeyBUnavailableError{Reason: "corrupt record", Err: cause}
		if !strings.Contains(err.Error(), "corrupt record") || !strings.Contains(err.Error(), "decode failed") {
			t.Errorf("expected both the reason and the cause in the message, got %q", err.Error())
		}
		if !errors.Is(err, cause) {
			t.Errorf("expected errors.Is to reach the underlying cause via Unwrap")
		}
	})
}

func TestIssuerNotTrustedError_Error(t *testing.T) {
	t.Run("without an underlying error", func(t *testing.T) {
		err := &IssuerNotTrustedError{TenantID: "acme"}
		if !strings.Contains(err.Error(), "tenant acme's registered CA") {
			t.Errorf("unexpected message: %q", err.Error())
		}
		if err.Unwrap() != nil {
			t.Errorf("expected Unwrap() to be nil with no underlying error")
		}
	})

	t.Run("with an underlying error", func(t *testing.T) {
		cause := errors.New("lookup failed")
		err := &IssuerNotTrustedError{TenantID: "acme", Err: cause}
		if !strings.Contains(err.Error(), "acme") || !strings.Contains(err.Error(), "lookup failed") {
			t.Errorf("expected both the tenant ID and the cause in the message, got %q", err.Error())
		}
		if !errors.Is(err, cause) {
			t.Errorf("expected errors.Is to reach the underlying cause via Unwrap")
		}
	})
}
