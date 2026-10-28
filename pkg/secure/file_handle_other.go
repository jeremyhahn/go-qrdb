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

//go:build !unix

package secure

import "os"

// openSecretFile opens path read-only. Outside unix there is no FIFO in the
// file namespace whose open waits for a writer: a Windows named pipe lives
// under \\.\pipe\ and opening it fails at once when no server instance is
// listening, so a plain open cannot stall, and checkSecretFile refuses
// anything but a regular file on the descriptor.
func openSecretFile(path string) (*os.File, error) {
	return os.Open(path) //nolint:gosec // the path is an operator-configured secret reference
}

// checkSecretOwner has no POSIX owner to compare outside unix. Windows
// reports no owner-only permission bits either (every file reads as 0666 or
// 0444), so checkSecretFile's mode check already refuses every file there and
// a secret file reference fails closed.
func checkSecretOwner(string, os.FileInfo) error {
	return nil
}
