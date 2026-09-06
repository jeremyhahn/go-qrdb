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

package discovery

import "syscall"

// newDatagramSocket creates an AF_INET datagram socket with close-on-exec set,
// and non-blocking mode when nonblock is true.
//
// darwin's socket(2) defines no SOCK_CLOEXEC or SOCK_NONBLOCK type flags — a
// Linux extension the BSDs adopted and darwin did not — so both are applied
// after the fact with fcntl. The
// socket and the FD_CLOEXEC that follows it are taken under ForkLock so that a
// concurrent fork+exec cannot inherit the descriptor in the window between
// them; this is the discipline package net uses on the same platforms and for
// the same reason. Non-blocking mode needs no such guard: an inherited
// descriptor is the hazard, and by then it cannot be inherited.
func newDatagramSocket(nonblock bool) (int, error) {
	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return -1, err
	}

	if nonblock {
		if err := syscall.SetNonblock(fd, true); err != nil {
			closeSocket(fd, "set non-blocking")
			return -1, err
		}
	}
	return fd, nil
}
