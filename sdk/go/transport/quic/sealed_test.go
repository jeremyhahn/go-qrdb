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

package quic

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func sealedBody(t *testing.T, st uint16, info *wire.ErrorInfo) []byte {
	t.Helper()
	body, err := wire.EncodeErrorPayload(st, wire.ErrorPayload{Message: sealedwindow.SealedMessage, Info: info})
	if err != nil {
		t.Fatalf("encode error payload: %v", err)
	}
	return body
}

// A sealed node's refusal is StatusUnavailable carrying the qrdb SEALED
// reason; the SDK reports it as *transport.BarrierSealedError so a caller can
// tell "up and sealed" from "draining" or "down".
func TestMapWireError_SealedRefusal(t *testing.T) {
	sealed := &wire.ErrorInfo{Domain: sealedwindow.ErrorDomain, Reason: sealedwindow.ReasonSealed}
	err := mapWireError("kv.Get", wire.StatusUnavailable, sealedBody(t, wire.StatusUnavailable, sealed))
	if !transport.IsBarrierSealed(err) {
		t.Fatalf("err = %T %v, want *transport.BarrierSealedError", err, err)
	}
	if !reachableButSealed(err) {
		t.Fatal("a sealed refusal is not counted as reachable")
	}
}

// Unavailable alone, or with another reason or domain, is not the sealed
// refusal: a draining node answers Unavailable too.
func TestMapWireError_UnavailableWithoutTheSealedReasonIsNotSealed(t *testing.T) {
	for name, info := range map[string]*wire.ErrorInfo{
		"no info":      nil,
		"other reason": {Domain: sealedwindow.ErrorDomain, Reason: "DRAINING"},
		"other domain": {Domain: "example.com", Reason: sealedwindow.ReasonSealed},
	} {
		err := mapWireError("kv.Get", wire.StatusUnavailable, sealedBody(t, wire.StatusUnavailable, info))
		if transport.IsBarrierSealed(err) || reachableButSealed(err) {
			t.Errorf("%s: err = %v reported as sealed", name, err)
		}
	}
	sealed := &wire.ErrorInfo{Domain: sealedwindow.ErrorDomain, Reason: sealedwindow.ReasonSealed}
	if err := mapWireError("kv.Get", wire.StatusFailedPrecondition, sealedBody(t, wire.StatusFailedPrecondition, sealed)); transport.IsBarrierSealed(err) {
		t.Errorf("a non-Unavailable status with the sealed reason reported as sealed: %v", err)
	}
}
