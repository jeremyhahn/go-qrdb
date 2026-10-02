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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// authCause returns the server-side cause of a refused submission: the
// verifier's error a *seal.HolderAuthenticationError keeps for the log and
// audit. The error itself is opaque and does not unwrap to it.
func authCause(t *testing.T, err error) error {
	t.Helper()
	var authErr *seal.HolderAuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("got %v, want *seal.HolderAuthenticationError", err)
	}
	return authErr.Err
}

// requireAuthRefused fails unless err is a refused submission, whatever its
// server-side cause -- the decoy cases, whose cause depends on how the
// stranger's answer is wrong.
func requireAuthRefused(t *testing.T, err error) {
	t.Helper()
	if cause := authCause(t, err); cause == nil {
		t.Fatal("refused submission carries no cause")
	}
}

// requireAuthCause fails unless err is a refused submission whose server-side
// cause is target.
func requireAuthCause(t *testing.T, err, target error, msgAndArgs ...any) {
	t.Helper()
	if cause := authCause(t, err); !errors.Is(cause, target) {
		t.Fatalf("submission refused for %v, want %v %v", cause, target, msgAndArgs)
	}
}
