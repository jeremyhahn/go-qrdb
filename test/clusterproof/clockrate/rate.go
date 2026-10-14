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

//go:build clusterproof

// Package clockrate changes how fast a Linux kernel's clocks run. It is the
// guest half of the clock-rate nemesis: the harness runs `clusterproof
// clock-rate` on a replica's own kernel through the target's exec template,
// and this package sets that kernel's tick and frequency through adjtimex(2).
//
// The tick is the microseconds the clocks advance on each timer interrupt,
// nominally 1e6/USER_HZ. Every Linux architecture reports USER_HZ as 100, so
// the nominal tick is 10000 and one microsecond of tick is 100 parts per
// million of rate; the kernel accepts a tick within ten percent of nominal.
// The frequency offset carries the remainder below one tick step, in ppm
// scaled by 2^16, within the kernel's ±500 ppm. Both govern CLOCK_MONOTONIC,
// the clock Go's timers and the Raft lease run on.
//
// adjtimex(2) is not namespaced: it needs CAP_SYS_TIME and changes the rate
// of the whole kernel, every container on it included. Check refuses a kernel
// that lacks the capability and the kernel the harness itself runs on, which
// it recognises by its boot ID.
package clockrate

import "strconv"

const (
	// NominalTick is the kernel tick, in microseconds, of a clock running
	// at its nominal rate.
	NominalTick = 10000
	// MaxPPM is the largest rate change ForPPM accepts, the tick's range.
	MaxPPM = 100_000

	ppmPerTick = 100
	freqScale  = 1 << 16
)

// Rate is a kernel clock rate as adjtimex(2) sets it.
type Rate struct {
	// Tick is the microseconds added to the clocks per timer interrupt.
	Tick int64
	// Freq is the frequency offset in ppm scaled by 2^16.
	Freq int64
}

// Nominal is the rate of a clock with no rate change applied.
var Nominal = Rate{Tick: NominalTick}

// ForPPM returns the rate that changes a clock by ppm parts per million:
// -5000 makes it run 0.5% slow, 20000 2% fast, 0 is Nominal. Whole hundreds
// of ppm go to the tick and the remainder to the frequency offset.
func ForPPM(ppm int) (Rate, error) {
	if ppm < -MaxPPM || ppm > MaxPPM {
		return Rate{}, &UsageError{Reason: "ppm " + strconv.Itoa(ppm) + " is outside ±" + strconv.Itoa(MaxPPM)}
	}
	return Rate{
		Tick: NominalTick + int64(ppm/ppmPerTick),
		Freq: int64(ppm%ppmPerTick) * freqScale,
	}, nil
}

// PPM is the rate change r applies, in parts per million.
func (r Rate) PPM() float64 {
	return float64((r.Tick-NominalTick)*ppmPerTick) + float64(r.Freq)/freqScale
}

// String reports the tick, the frequency offset and the total in ppm.
func (r Rate) String() string {
	return "tick " + strconv.FormatInt(r.Tick, 10) +
		" freq " + strconv.FormatFloat(float64(r.Freq)/freqScale, 'f', 3, 64) + " ppm" +
		" (" + strconv.FormatFloat(r.PPM(), 'f', 3, 64) + " ppm)"
}

// Kernel is the clock interface of one kernel.
type Kernel interface {
	// BootID identifies the kernel: every container on it reads the same.
	BootID() (string, error)
	// SysTime reports whether the process holds CAP_SYS_TIME.
	SysTime() (bool, error)
	// Rate reads the kernel's clock rate.
	Rate() (Rate, error)
	// SetRate sets the kernel's clock rate.
	SetRate(Rate) error
}

// Check returns nil if the clock rate of k may be changed by a harness
// running on the kernel with boot ID harness: k must not be that kernel, and
// the process must hold CAP_SYS_TIME. An empty harness ID cannot prove the
// kernels differ and is refused.
func Check(k Kernel, harness string) error {
	if harness == "" {
		return &UsageError{Reason: "the harness kernel's boot ID is required"}
	}
	id, err := k.BootID()
	if err != nil {
		return &KernelError{Op: "boot id", Err: err}
	}
	if id == harness {
		return ErrHarnessKernel
	}
	ok, err := k.SysTime()
	if err != nil {
		return &KernelError{Op: "capget", Err: err}
	}
	if !ok {
		return ErrNoSysTime
	}
	return nil
}

// Set checks k as Check does, then sets its clock rate to r and returns the
// rate it replaced.
func Set(k Kernel, r Rate, harness string) (Rate, error) {
	if err := Check(k, harness); err != nil {
		return Rate{}, err
	}
	prev, err := k.Rate()
	if err != nil {
		return Rate{}, &KernelError{Op: "adjtimex read", Err: err}
	}
	if err := k.SetRate(r); err != nil {
		return prev, &KernelError{Op: "adjtimex set", Err: err}
	}
	return prev, nil
}
