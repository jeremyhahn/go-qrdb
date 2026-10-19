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

package rest

import (
	"crypto/tls"
	"strings"
	"testing"
)

func TestErrorMessages_NameTheirCause(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want []string
	}{
		{&ListenerHostMismatchError{TCPHost: "10.0.0.1", UDPHost: "0.0.0.0"}, []string{"0.0.0.0", "10.0.0.1"}},
		{&TLSNotConfiguredError{}, []string{"TLS-only", "WithTLS"}},
		{&WeakTLSVersionError{MinVersion: tls.VersionTLS12}, []string{"TLS 1.2", "TLS 1.3"}},
		{&CABootstrapConflictError{}, []string{"/v1/ca/bootstrap", "exactly one"}},
		{&ClusterJoinMissingTokenError{}, []string{"rest: "}},
	} {
		msg := tc.err.Error()
		for _, w := range tc.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%T.Error() = %q, want it to contain %q", tc.err, msg, w)
			}
		}
	}
}

func TestTLSVersionName_EveryVersion(t *testing.T) {
	for v, want := range map[uint16]string{
		tls.VersionTLS10: "TLS 1.0",
		tls.VersionTLS11: "TLS 1.1",
		tls.VersionTLS12: "TLS 1.2",
		tls.VersionTLS13: "TLS 1.3",
		0:                "unset",
		0xffff:           "unknown",
	} {
		if got := tlsVersionName(v); got != want {
			t.Errorf("tlsVersionName(%#x) = %q, want %q", v, got, want)
		}
	}
}
