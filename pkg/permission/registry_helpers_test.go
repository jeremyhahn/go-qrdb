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

package permission

// unregisterForTesting removes the given permissions from the registry. It
// exists so a test that registers a probe permission can leave the
// process-global registry as it found it: TestEveryPermissionDeclaresItsScope
// walks All(), and a probe left behind by an earlier test reads as an
// undeclared built-in. Production code never unregisters a permission, so the
// helper lives in a test file and is absent from the binary.
func unregisterForTesting(perms ...Permission) {
	mu.Lock()
	defer mu.Unlock()
	for _, p := range perms {
		delete(registry, p)
	}
}
