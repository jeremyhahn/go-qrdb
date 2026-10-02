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

package statemachine

import (
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

func TestReservedKeyRefusal_RefusesA0x00KeyAsInvalid(t *testing.T) {
	res, refused := ReservedKeyRefusal([]byte("ok"), []byte("\x00qrdb.lease/seq"))
	if !refused {
		t.Fatal("a 0x00 key was not refused")
	}
	msg, kind, ok := protocol.DecodeAppErrorKind(res.Data)
	if !ok || kind != protocol.AppErrorInvalid || !strings.Contains(msg, "reserved 0x00 namespace") {
		t.Fatalf("refusal = %q kind %v, want an invalid-input refusal naming the namespace", msg, kind)
	}
}

func TestReservedKeyRefusal_PassesOrdinaryAndEmptyKeys(t *testing.T) {
	if _, refused := ReservedKeyRefusal([]byte("a"), []byte{}, nil, []byte("b\x00")); refused {
		t.Fatal("an ordinary key was refused")
	}
}

func TestOpKeys_ListsEveryOpKeyInOrder(t *testing.T) {
	keys := OpKeys([]protocol.DecodedOp{{Key: []byte("a")}, {Key: []byte("b")}})
	if len(keys) != 2 || string(keys[0]) != "a" || string(keys[1]) != "b" {
		t.Fatalf("OpKeys = %q", keys)
	}
	if len(OpKeys(nil)) != 0 {
		t.Fatal("OpKeys(nil) is not empty")
	}
}
