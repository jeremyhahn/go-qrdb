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

package ceremony

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestErrorMessages pins the message of each typed error no path in the
// package's other tests renders, so an operator-facing string cannot drift
// silently.
func TestErrorMessages(t *testing.T) {
	cause := errors.New("boom")
	tests := map[string]struct {
		err  error
		want string
	}{
		"IssueCertKeyError": {
			err:  &IssueCertKeyError{Field: "a public key"},
			want: "ceremony: certificate issuance requires a public key",
		},
		"NilCeremonyCAProviderError": {
			err:  &NilCeremonyCAProviderError{},
			want: "ceremony: nil cakey.Provider",
		},
		"ProviderCASignerError": {
			err:  &ProviderCASignerError{Err: cause},
			want: "ceremony: resolving the CA signer failed: boom",
		},
		"ProviderCACertificateError": {
			err:  &ProviderCACertificateError{Err: cause},
			want: "ceremony: resolving the CA certificate failed: boom",
		},
		"SystemBarrierCredentialMismatchError": {
			err:  &SystemBarrierCredentialMismatchError{Cause: cause},
			want: "ceremony: system barrier is already unsealed under a different credential: boom",
		},
		"HolderLimitReachedError": {
			err:  &HolderLimitReachedError{Total: 3},
			want: "ceremony: all 3 share holders the barrier is split to are enrolled; no more can be enrolled before it is initialized",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.err.Error())
		})
	}
}

// TestErrorUnwrap checks that each wrapping error exposes its cause to
// errors.Is, and that a different error does not match.
func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("boom")
	other := errors.New("other")
	tests := map[string]error{
		"ProviderCASignerError":                &ProviderCASignerError{Err: cause},
		"ProviderCACertificateError":           &ProviderCACertificateError{Err: cause},
		"SystemBarrierCredentialMismatchError": &SystemBarrierCredentialMismatchError{Cause: cause},
	}
	for name, err := range tests {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, err, cause)
			assert.NotErrorIs(t, err, other)
		})
	}
}
