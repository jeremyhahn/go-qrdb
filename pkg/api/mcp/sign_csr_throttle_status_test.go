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

package mcp

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A throttled ceremony-sign-csr is RESOURCE_EXHAUSTED on MCP.
func TestServiceErrorCode_SignCSRThrottled(t *testing.T) {
	if got := ServiceErrorCode(&service.SignCSRThrottledError{RetryAfter: time.Second}); got != wire.CodeResourceExhausted {
		t.Fatalf("code = %s, want RESOURCE_EXHAUSTED", got)
	}
}
