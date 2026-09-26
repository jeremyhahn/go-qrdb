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

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// requireErrorAs asserts that err's chain carries a T and returns it.
//
// It is the assertion the vector tests make about the service's typed error
// vocabulary (go-qrdb#331): the TYPE is the contract every transport
// classifies, so it is what a test pins — never a code field or a message.
// Untagged, because callers in every build-tag variant use it.
func requireErrorAs[T error](t *testing.T, err error) T {
	t.Helper()
	var target T
	require.ErrorAs(t, err, &target)
	return target
}
