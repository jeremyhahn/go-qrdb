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

package mcp

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A sealed node refuses a tool with UNAVAILABLE and the SEALED reason; the SDK
// reports it as *transport.BarrierSealedError.
func TestMapToolError_SealedRefusal(t *testing.T) {
	err := mapToolError("qrdb_get", &wire.ErrorPayload{Code: wire.CodeUnavailable, Message: sealedwindow.SealedMessage, Reason: sealedwindow.ReasonSealed})
	if !transport.IsBarrierSealed(err) {
		t.Fatalf("err = %T %v, want *transport.BarrierSealedError", err, err)
	}
}

// UNAVAILABLE without the SEALED reason (a draining node), or SEALED on
// another code, is not the sealed refusal.
func TestMapToolError_NotSealedWithoutBoth(t *testing.T) {
	for name, payload := range map[string]*wire.ErrorPayload{
		"unavailable, no reason": {Code: wire.CodeUnavailable, Message: "draining"},
		"other code":             {Code: wire.CodeInternal, Message: "barrier sealed", Reason: sealedwindow.ReasonSealed},
	} {
		if err := mapToolError("qrdb_get", payload); transport.IsBarrierSealed(err) {
			t.Errorf("%s: reported as sealed: %v", name, err)
		}
	}
}
