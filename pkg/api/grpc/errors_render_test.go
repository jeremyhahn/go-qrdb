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

package grpc

import (
	"crypto/tls"
	"strings"
	"testing"
)

// TestStartupRefusals_Render pins the operator-facing text of the refusals
// Start and the ceremony path raise. No build constraint: every tag set the
// package builds under carries these types.
func TestStartupRefusals_Render(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&CeremonyCANotConfiguredError{}, "grpc: ceremony CA is not configured"},
		{&TLSNotConfiguredError{}, "grpc: no TLS configured; the gRPC transport is TLS-only, call WithTLS with a TLS 1.3+ config"},
		{&WeakTLSVersionError{MinVersion: tls.VersionTLS12}, "grpc: TLS MinVersion TLS 1.2 is below the required TLS 1.3"},
		{&ServiceNotConfiguredError{Service: "sql", Hint: "requires mode: raft"}, "grpc: sql service is not configured: requires mode: raft"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%T.Error() = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestTLSVersionName names every version a caller can configure, and falls
// back to the hex constant for one it cannot.
func TestTLSVersionName(t *testing.T) {
	for v, want := range map[uint16]string{
		tls.VersionTLS10: "TLS 1.0",
		tls.VersionTLS11: "TLS 1.1",
		tls.VersionTLS12: "TLS 1.2",
		tls.VersionTLS13: "TLS 1.3",
		0:                "unset",
	} {
		if got := tlsVersionName(v); got != want {
			t.Errorf("tlsVersionName(%#04x) = %q, want %q", v, got, want)
		}
	}
	if got := tlsVersionName(0x0305); !strings.HasPrefix(got, "0x") {
		t.Errorf("an unknown version renders %q, want its hex constant", got)
	}
}
