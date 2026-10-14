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

//go:build clusterproof && linux

package clockrate

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const bootIDPath = "/proc/sys/kernel/random/boot_id"

// System is the Kernel this process runs on.
type System struct{}

var _ Kernel = System{}

// BootID reads the kernel's boot ID, which every container on it shares.
func (System) BootID() (string, error) {
	b, err := os.ReadFile(bootIDPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// SysTime reports whether CAP_SYS_TIME is in this process's effective set.
func (System) SysTime() (bool, error) {
	// Version 3 capabilities are two 32-bit words; the kernel writes both.
	var data [2]unix.CapUserData
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return false, err
	}
	return data[unix.CAP_SYS_TIME/32].Effective&(1<<(unix.CAP_SYS_TIME%32)) != 0, nil
}

// Rate reads the kernel's tick and frequency offset without changing them.
func (System) Rate() (Rate, error) {
	var tx unix.Timex
	if _, err := unix.Adjtimex(&tx); err != nil {
		return Rate{}, err
	}
	return Rate{Tick: int64(tx.Tick), Freq: int64(tx.Freq)}, nil
}

// SetRate sets the kernel's tick and frequency offset.
func (System) SetRate(r Rate) error {
	tx := unix.Timex{Modes: unix.ADJ_TICK | unix.ADJ_FREQUENCY}
	setField(&tx.Tick, r.Tick)
	setField(&tx.Freq, r.Freq)
	_, err := unix.Adjtimex(&tx)
	return err
}

// setField stores v in a Timex field, whose width follows the platform's C
// long.
func setField[T ~int32 | ~int64](dst *T, v int64) { *dst = T(v) }
