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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

func TestCheckResultError_TxnReadRefusalIsTyped(t *testing.T) {
	err := checkResultError(sm.Result{Data: protocol.EncodeTxnReadRefusal(4<<20, 4<<20+3)})
	var tooLarge *TxnReadResultTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Limit != 4<<20 || tooLarge.Read != 4<<20+3 {
		t.Fatalf("checkResultError = %v, want *TxnReadResultTooLargeError{Limit: %d, Read: %d}", err, 4<<20, 4<<20+3)
	}
}

func TestTxnReadResultTooLargeError_NamesBothFigures(t *testing.T) {
	msg := (&TxnReadResultTooLargeError{Limit: 100, Read: 123}).Error()
	if !strings.HasPrefix(msg, "service: ") || !strings.Contains(msg, "100") || !strings.Contains(msg, "123") {
		t.Fatalf("message %q does not name its origin, the limit and the bytes read", msg)
	}
}
