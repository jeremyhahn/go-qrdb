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

//go:build !unix

package keywrap

// openNonBlockFlag is the non-Unix counterpart to nonblock_unix.go's
// constant of the same name: a no-op. Named pipes are not a concept
// os.Root.OpenFile has to defend against identically on these platforms
// (notably GOOS=windows, which Root itself already documents separate
// behavior for).
const openNonBlockFlag = 0
