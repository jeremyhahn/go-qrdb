// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package secure

// contents returns a heap copy of b's bytes for a test assertion, or nil when
// b is nil, destroyed or empty. It carries no build constraint because every
// test file in this package calls it.
func contents(b *Buffer) []byte {
	out, err := CopyOut(b)
	if err != nil {
		return nil
	}
	return out
}
