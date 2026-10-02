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
	"testing"
)

func TestMVCCCompactNodeRoundtrip(t *testing.T) {
	for _, rev := range []int64{1, 42, -3} {
		encoded, err := EncodeMVCCCompactNode(rev)
		if err != nil || encoded[0] != OpMVCCCompactNode || len(encoded) != 9 {
			t.Fatalf("EncodeMVCCCompactNode(%d) = %v, %v", rev, encoded, err)
		}
		cmd, err := DecodeCommand(encoded)
		if err != nil {
			t.Fatalf("DecodeCommand: %v", err)
		}
		got, err := DecodeMVCCCompactNode(cmd.Payload)
		if err != nil || got != rev {
			t.Fatalf("DecodeMVCCCompactNode = %d, %v; want %d", got, err, rev)
		}
	}
}

func TestDecodeMVCCCompactNode_RefusesAnyOtherLength(t *testing.T) {
	for _, payload := range [][]byte{nil, {1, 2, 3}, {1, 0, 0, 0, 0, 0, 0, 0, 0}} {
		var ice *InvalidCommandError
		if _, err := DecodeMVCCCompactNode(payload); !errors.As(err, &ice) {
			t.Fatalf("payload %v = %v", payload, err)
		}
	}
}
