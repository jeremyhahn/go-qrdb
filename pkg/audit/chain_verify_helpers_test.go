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

package audit

import "context"

// verifyChainFile verifies one file of a rotation set on its own, reporting
// *ChainBrokenError at the first record that does not follow its predecessor.
//
// It says nothing about where the file sits in the set -- that is
// verifyChainSetAt's question. Production verifies whole sets; the tests use this
// to assert on a single file.
func verifyChainFile(ctx context.Context, dir *trailDir, path string) error {
	_, err := verifyChainSegment(ctx, dir, path)
	return err
}
