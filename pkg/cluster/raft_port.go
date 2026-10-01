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
	"net"
	"strconv"
)

// CheckRaftTransportPort refuses port 0 in the address of a multi-node Raft
// transport. It is the one socket an embedder cannot pre-bind: go-quicraft
// binds it itself, and every peer dials the ADVERTISED address, so a
// kernel-assigned port would be advertised as ":0" and no peer could reach the
// replica (go-qrdb#374). field names the setting in the caller's vocabulary
// (a config key or a flag). An address that does not parse is left to the
// validation that owns it.
func CheckRaftTransportPort(field, addr string) error {
	if _, port, err := net.SplitHostPort(addr); err == nil && port == "0" {
		return &RaftPortZeroError{Field: field, Address: addr}
	}
	return nil
}

// RaftPortZeroError is returned by CheckRaftTransportPort.
type RaftPortZeroError struct {
	// Field is the setting that named port 0.
	Field string
	// Address is the address it named.
	Address string
}

// Error returns a human-readable description.
func (e *RaftPortZeroError) Error() string {
	return "cluster: " + e.Field + " " + strconv.Quote(e.Address) +
		": port 0 is not supported for a multi-node Raft transport: go-quicraft binds the " +
		"socket itself and peers dial the advertised port; reserve a fixed port"
}
