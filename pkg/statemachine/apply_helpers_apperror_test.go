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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// A not-configured subsystem must be DECODABLE as an application error, not
// left as bare text in the result payload.
//
// The payload has one reader — service.checkResultError — and it recognises an
// application error only by protocol's prefix. Encoded bare, the error never
// decodes: the proposer reads the error text as an ordinary result, and
// TransactionService.Execute reports Succeeded=false with a nil error.
//
// That is not "the subsystem is missing" to a caller. A PutIfAbsent returning
// (false, nil) means THE KEY ALREADY EXISTS, so a misconfiguration is reported
// as another writer having won the race — and the caller goes on to read a key
// that was never written. go-xkms's CA key store hit exactly this and could not
// boot (go-qrdb#62).
func TestResolveApplyError_NotConfiguredIsEncodedAsADecodableAppError(t *testing.T) {
	handlerErr := &ServiceNotConfiguredError{Service: "mvcc"}

	result, stop, err := resolveApplyError(nil, 3, sm.Result{}, handlerErr)

	// It must NOT go back to Raft: a non-nil Update error is a fatal apply
	// failure that stalls every subsequent read on the shard.
	if stop || err != nil {
		t.Fatalf("resolveApplyError returned stop=%v err=%v; a not-configured "+
			"subsystem must be reported through the result, never to Raft", stop, err)
	}

	msg, ok := protocol.DecodeAppError(result.Data)
	if !ok {
		t.Fatalf("result payload %q does not decode as an application error; "+
			"unprefixed, its only reader treats it as an ordinary result and the "+
			"caller reads a misconfiguration as a failed compare", result.Data)
	}
	if !strings.Contains(msg, "mvcc") {
		t.Fatalf("decoded message %q does not name the missing service", msg)
	}
}

// Any other handler error still goes to Raft. The special case is exactly one
// error type, and widening it would swallow genuine infrastructure failures.
func TestResolveApplyError_OtherErrorsStillStopApply(t *testing.T) {
	sentinel := errors.New("storage is broken")

	_, stop, err := resolveApplyError(nil, 3, sm.Result{}, sentinel)
	if !stop || !errors.Is(err, sentinel) {
		t.Fatalf("resolveApplyError returned stop=%v err=%v; a genuine failure must "+
			"halt Apply rather than being encoded as a result", stop, err)
	}
}
