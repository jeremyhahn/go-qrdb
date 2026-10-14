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

// Package indexkey holds the key-namespace constants and byte helpers shared
// by the secondary-index implementations in pkg/index and its backend
// subpackages, so each of them writes the same "idx:" namespace.
package indexkey

// Prefix is the index key namespace prefix.
const Prefix = "idx:"

// Separator separates components within an index key.
const Separator = ":"

// Successor returns a copy of b with its value incremented by one, for use
// as an exclusive upper bound in range scans. If all bytes are 0xFF, it
// returns a slice with an appended 0x00 byte so the upper bound stays valid.
func Successor(b []byte) []byte {
	result := make([]byte, len(b))
	copy(result, b)
	for i := len(result) - 1; i >= 0; i-- {
		if result[i] < 0xFF {
			result[i]++
			return result[:i+1]
		}
	}
	// All bytes are 0xFF; append a zero byte to create a valid upper bound.
	return append(result, 0x00)
}
