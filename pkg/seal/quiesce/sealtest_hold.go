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

//go:build qrdb_sealtest

// The qrdb_sealtest build tag compiles a hold seam into qrdbd for the
// seal-drain integration test, and no release build carries the tag. The
// daemon looks for a quiesce-sealtest directory in its working directory
// holding three FIFOs (held, draining, release) and a file arm naming a
// lease. The first operation that lease admits after arm is written writes
// the lease's name to held and blocks, inside its lease, until release
// reaches EOF. A drain that then finds leases held writes their names to
// draining before it waits. Without the directory both hooks stay nil.

package quiesce

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// sealtestDir is the hold directory, relative to the daemon's working
// directory.
const sealtestDir = "quiesce-sealtest"

func init() {
	dir, err := filepath.Abs(sealtestDir)
	if err != nil {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "held")); err != nil {
		return
	}
	var (
		fired    atomic.Bool
		holdOnce sync.Once
		drainOne sync.Once
	)
	acquiredHook = func(l *Lease) {
		if fired.Load() {
			return
		}
		armed, err := os.ReadFile(filepath.Join(dir, "arm"))
		if err != nil || strings.TrimSpace(string(armed)) != l.name {
			return
		}
		holdOnce.Do(func() {
			fired.Store(true)
			fifoWrite(filepath.Join(dir, "held"), l.name)
			fifoDrain(filepath.Join(dir, "release"))
		})
	}
	drainWaitHook = func(held []string) {
		if !fired.Load() {
			return
		}
		drainOne.Do(func() {
			fifoWrite(filepath.Join(dir, "draining"), strings.Join(held, "\n"))
		})
	}
}

// fifoWrite opens the FIFO at path, which blocks until the test reads it,
// and writes s.
func fifoWrite(path, s string) {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	_, _ = f.WriteString(s)
	_ = f.Close()
}

// fifoDrain reads the FIFO at path until the test closes its end.
func fifoDrain(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	buf := make([]byte, 64)
	for {
		if _, err := f.Read(buf); err != nil {
			break
		}
	}
	_ = f.Close()
}
