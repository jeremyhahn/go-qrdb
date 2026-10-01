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

package cluster

import (
	"errors"
	"testing"
)

func TestCheckRaftTransportPort(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		refused bool
	}{
		{name: "fixed port", addr: "127.0.0.1:63001"},
		{name: "port zero", addr: "127.0.0.1:0", refused: true},
		{name: "wildcard port zero", addr: "0.0.0.0:0", refused: true},
		{name: "unparseable is left to its own validation", addr: "no-port"},
		{name: "empty", addr: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckRaftTransportPort("raft.raft_address", tc.addr)
			if !tc.refused {
				if err != nil {
					t.Fatalf("CheckRaftTransportPort(%q) = %v, want nil", tc.addr, err)
				}
				return
			}
			var zero *RaftPortZeroError
			if !errors.As(err, &zero) || zero.Field != "raft.raft_address" || zero.Address != tc.addr {
				t.Fatalf("CheckRaftTransportPort(%q) = %v, want *RaftPortZeroError", tc.addr, err)
			}
		})
	}
}

func TestRaftPortZeroError_Error(t *testing.T) {
	got := (&RaftPortZeroError{Field: "--listen", Address: "0.0.0.0:0"}).Error()
	want := `cluster: --listen "0.0.0.0:0": port 0 is not supported for a multi-node Raft transport: ` +
		"go-quicraft binds the socket itself and peers dial the advertised port; reserve a fixed port"
	if got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
