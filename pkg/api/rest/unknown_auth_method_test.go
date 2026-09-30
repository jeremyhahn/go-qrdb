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
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// An unknown permitted-method token is answered 400 over REST, the same as
// any other user validation failure.
func TestUnknownAuthMethod_IsBadRequest(t *testing.T) {
	if got := ServiceErrorStatus(&user.UnknownAuthMethodError{Method: "totp"}); got != http.StatusBadRequest {
		t.Errorf("ServiceErrorStatus = %d, want 400", got)
	}
}
