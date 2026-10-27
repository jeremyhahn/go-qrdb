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

// Command leanroot is a client that imports the sdk/go root and nothing else
// from QRDB: the client handle, the closed-store error and the MCP error
// codes, the types every caller of a remote node names.
//
// It is the fixture of the lean-root build gate
// (test/buildgates/lean_root_test.go, `make check-lean-standalone`): whatever
// this program imports, every caller of the sdk/go root compiles, so the gate
// reads this package's import graph and fails when it reaches the protocol
// servers in pkg/server or pkg/api. Those live behind sdk/go/server.
//
// Usage:
//
//	leanroot
package main

import (
	"fmt"

	qrdb "github.com/jeremyhahn/go-qrdb/sdk/go"
)

func main() {
	var client *qrdb.Client
	fmt.Printf("client set: %t, closed-store error recognised: %t, MCP codes: %d\n",
		client != nil, qrdb.IsStorageClosed(qrdb.ErrStorageClosed), len(qrdb.MCPCodes()))
}
