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

package engine

import (
	"bytes"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// deleteRangeOps is the batch of opType deletes a range delete over keys
// stands for, in key order so the watch events it emits are too.
func deleteRangeOps(opType byte, keys [][]byte) []protocol.DecodedOp {
	slices.SortFunc(keys, bytes.Compare)
	ops := make([]protocol.DecodedOp, len(keys))
	for i, k := range keys {
		ops[i] = protocol.DecodedOp{Type: opType, Key: k}
	}
	return ops
}
