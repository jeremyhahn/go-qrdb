// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package clustermig

import (
	"errors"
	"io/fs"
	"os"
)

// fileOwner cannot determine a file's owner off unix, so every credential
// file is refused there.
func fileOwner(fs.FileInfo) (int, bool) { return 0, false }

// errNoSafeOpen is why a credential file is refused on a platform that
// cannot open it without following a symlink.
var errNoSafeOpen = errors.New("clustermig: credential files cannot be opened safely on this platform")

// openCredentialDir refuses off unix, where the directory cannot be opened
// without following a symlink.
func openCredentialDir(string) (*os.File, error) { return nil, errNoSafeOpen }

// openCredentialAt refuses off unix; it is never reached there, because
// openCredentialDir refuses first.
func openCredentialAt(*os.File, string) (*os.File, error) { return nil, errNoSafeOpen }
