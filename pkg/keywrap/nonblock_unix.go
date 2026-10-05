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

//go:build unix

package keywrap

import "syscall"

// openNonBlockFlag is OR'd into readKeyFile's Open flags so opening a
// named pipe (FIFO) planted inside the confined key directory never
// blocks waiting for a writer: POSIX requires O_NONBLOCK|O_RDONLY to
// return immediately regardless of whether a writer has opened the
// other end. It is a no-op on a regular file.
const openNonBlockFlag = syscall.O_NONBLOCK
