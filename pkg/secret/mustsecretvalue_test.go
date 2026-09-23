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

package secret_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secret"
)

// mustSecretValue is secret.NewSecretValue for tests that construct a fixture
// they already know must seal successfully — the vast majority of this
// package's tests. NewSecretValue started returning an error alongside its
// *SecretValue (go-qrdb#114 review: a swallowed sealing failure let a
// credential silently become empty), and this keeps every call site that
// only cares about the happy path from repeating the same three-line check.
// A test that specifically drives the ERROR path calls secret.NewSecretValue
// directly instead of through this helper.
func mustSecretValue(t *testing.T, raw string, value []byte, strategy secret.ResolvedStrategy) *secret.SecretValue {
	t.Helper()
	sv, err := secret.NewSecretValue(raw, value, strategy)
	if err != nil {
		t.Fatalf("secret.NewSecretValue(%q, ..., %v): %v", raw, strategy, err)
	}
	return sv
}
