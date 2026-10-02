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

package unix

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
)

// TestWithPINBackend_RawBackendIsThrottled: a raw backend handed straight to
// this transport's WithPINBackend is decorated with the SO PIN lockout, so
// the SO PIN checks its handlers make are still throttled.
func TestWithPINBackend_RawBackendIsThrottled(t *testing.T) {
	srv := NewServer(nil, nil, "/nonexistent/qrdb.sock", WithPINBackend(pintest.NewSOPINBackend(t)))
	pintest.RequireThrottled(t, srv.pinBackend)
}
