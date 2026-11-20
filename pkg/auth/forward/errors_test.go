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

package forward

import (
	"errors"
	"strings"
	"testing"
)

// TestErrorMessagesNameTheirCause checks every typed error renders a message
// that identifies the package and the specific failure.
//
// These strings are the operator-facing half of the design: Verify collapses
// to an opaque 401/403 on the wire precisely so the detail lives only in logs,
// which makes the log message the sole diagnostic. A message that lost its
// field name would leave nothing to debug from.
func TestErrorMessagesNameTheirCause(t *testing.T) {
	cause := errors.New("underlying")

	tests := []struct {
		name     string
		err      error
		contains []string
	}{
		{
			name:     "encoding names the field and the cause",
			err:      &ForwardAssertionEncodingError{Field: "signature", Err: cause},
			contains: []string{"auth/forward", "signature", "underlying"},
		},
		{
			name:     "not-cluster-peer with a cause appends it",
			err:      &ForwardAssertionNotClusterPeerError{Err: cause},
			contains: []string{"not a cluster node", "underlying"},
		},
		{
			name:     "signature with a reason appends it",
			err:      &ForwardAssertionSignatureError{Reason: "verification failed"},
			contains: []string{"signature invalid", "verification failed"},
		},
		{
			name:     "expired names the reason",
			err:      &ForwardAssertionExpiredError{Reason: "expired"},
			contains: []string{"validity window", "expired"},
		},
		{
			name:     "binding mismatch names the field",
			err:      &ForwardAssertionBindingMismatchError{Field: "body"},
			contains: []string{"not bound to request", "body"},
		},
		{
			name:     "field error names the field",
			err:      &ForwardAssertionFieldError{Field: "subject"},
			contains: []string{"field invalid", "subject"},
		},
		{
			name:     "signing names the cause",
			err:      &ForwardSigningError{Err: cause},
			contains: []string{"failed to sign", "underlying"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("message %q does not contain %q", got, want)
				}
			}
		})
	}
}

// TestNoCauseVariantsDoNotRenderEmptyDetail pins the difference the two-branch
// Error methods exist for: with no cause there must be no dangling separator
// implying a detail that was never supplied.
func TestNoCauseVariantsDoNotRenderEmptyDetail(t *testing.T) {
	for _, err := range []error{
		&ForwardAssertionNotClusterPeerError{},
		&ForwardAssertionSignatureError{},
	} {
		if got := err.Error(); strings.HasSuffix(got, ": ") {
			t.Errorf("message %q ends in a separator with nothing after it", got)
		}
	}
}

// TestUnwrapReachesTheCause covers the errors.Is/As plumbing. Middleware
// matches on the wrapped cause to decide between a retry and a hard reject, so
// an Unwrap that dropped the chain would silently change that decision.
func TestUnwrapReachesTheCause(t *testing.T) {
	cause := errors.New("underlying")

	tests := []struct {
		name string
		err  error
	}{
		{"encoding", &ForwardAssertionEncodingError{Field: "assertion", Err: cause}},
		{"not-cluster-peer", &ForwardAssertionNotClusterPeerError{Err: cause}},
		{"signing", &ForwardSigningError{Err: cause}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, cause) {
				t.Fatalf("errors.Is could not reach the cause through %T", tc.err)
			}
		})
	}
}
