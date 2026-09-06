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

//go:build !darwin

package discovery

import "syscall"

// newDatagramSocket creates an AF_INET datagram socket with close-on-exec set,
// and non-blocking mode when nonblock is true.
//
// On these platforms socket(2) accepts SOCK_CLOEXEC and SOCK_NONBLOCK as part
// of the socket type, so both flags are set atomically with the descriptor and
// no window exists in which a concurrent fork could inherit it. darwin has no
// such flags and applies them with fcntl instead; see socket_darwin.go.
func newDatagramSocket(nonblock bool) (int, error) {
	typ := syscall.SOCK_DGRAM | syscall.SOCK_CLOEXEC
	if nonblock {
		typ |= syscall.SOCK_NONBLOCK
	}
	return syscall.Socket(syscall.AF_INET, typ, 0)
}
