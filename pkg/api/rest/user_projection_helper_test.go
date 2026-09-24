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

package rest

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// mustUserToResponse projects u with userToResponse, failing the test on a projection
// error, so a test about the projected fields reads one value.
func mustUserToResponse(t testing.TB, lister user.FactorLister, u *user.User) *userResponse {
	t.Helper()
	out, err := userToResponse(context.Background(), lister, u)
	if err != nil {
		t.Fatalf("userToResponse() error = %v", err)
	}
	return out
}
