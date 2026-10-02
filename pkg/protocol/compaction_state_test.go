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

package protocol

import (
	"errors"
	"strings"
	"testing"
)

func TestCompactionStateRoundTrip(t *testing.T) {
	cmds := []CompactionStateCommand{
		{Kind: CompactionStateSetPolicy, Scope: "acme/app/", Policy: []byte(`{"policy":{"mode":"revision","retainRevisions":5}}`)},
		{Kind: CompactionStateClear, Scope: ""},
		{Kind: CompactionStateSample, Scope: "\x00node", At: 1_700_000_000_000_000_000, Revision: 42},
	}
	for _, want := range cmds {
		encoded, err := EncodeCompactionState(want)
		if err != nil || encoded[0] != OpCompactionState {
			t.Fatalf("EncodeCompactionState(%+v) = %v, %v", want, encoded, err)
		}
		cmd, err := DecodeCommand(encoded)
		if err != nil {
			t.Fatalf("DecodeCommand: %v", err)
		}
		got, err := DecodeCompactionState(cmd.Payload)
		if err != nil {
			t.Fatalf("DecodeCompactionState: %v", err)
		}
		if got.Kind != want.Kind || got.Scope != want.Scope || string(got.Policy) != string(want.Policy) || got.At != want.At || got.Revision != want.Revision {
			t.Fatalf("round trip = %+v, want %+v", got, want)
		}
	}
}

func TestEncodeCompactionState_Refuses(t *testing.T) {
	for name, cmd := range map[string]CompactionStateCommand{
		"unknown kind":        {Kind: 99, Scope: "a/b/"},
		"set without policy":  {Kind: CompactionStateSetPolicy, Scope: "a/b/"},
		"scope too long":      {Kind: CompactionStateClear, Scope: strings.Repeat("x", 70000)},
		"clear with a policy": {Kind: CompactionStateClear, Scope: "a/b/", Policy: []byte("{}")},
	} {
		if _, err := EncodeCompactionState(cmd); err == nil {
			t.Fatalf("%s: EncodeCompactionState accepted %+v", name, cmd)
		}
	}
}

func TestDecodeCompactionState_RefusesMalformedPayloads(t *testing.T) {
	good, _ := EncodeCompactionState(CompactionStateCommand{Kind: CompactionStateSample, Scope: "a/b/", At: 1, Revision: 2})
	for name, payload := range map[string][]byte{
		"empty":            nil,
		"no scope length":  {byte(CompactionStateClear)},
		"short scope":      {byte(CompactionStateClear), 5, 0, 'a'},
		"unknown kind":     {9, 0, 0},
		"short sample":     append(append([]byte(nil), good[1:len(good)-1]...)),
		"clear with body":  {byte(CompactionStateClear), 0, 0, 1},
		"set without body": {byte(CompactionStateSetPolicy), 0, 0},
	} {
		var ice *InvalidCommandError
		if _, err := DecodeCompactionState(payload); !errors.As(err, &ice) {
			t.Fatalf("%s: DecodeCompactionState = %v, want *InvalidCommandError", name, err)
		}
	}
}
