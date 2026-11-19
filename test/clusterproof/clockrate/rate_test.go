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

package clockrate

import (
	"errors"
	"testing"
)

// fakeKernel is a Kernel in memory: it records every SetRate and never
// touches the host's clocks.
type fakeKernel struct {
	bootID  string
	bootErr error
	sysTime bool
	capErr  error
	rate    Rate
	readErr error
	setErr  error
	sets    []Rate
}

var _ Kernel = (*fakeKernel)(nil)

func (k *fakeKernel) BootID() (string, error) { return k.bootID, k.bootErr }
func (k *fakeKernel) SysTime() (bool, error)  { return k.sysTime, k.capErr }
func (k *fakeKernel) Rate() (Rate, error)     { return k.rate, k.readErr }

func (k *fakeKernel) SetRate(r Rate) error {
	k.sets = append(k.sets, r)
	if k.setErr != nil {
		return k.setErr
	}
	k.rate = r
	return nil
}

// guest is a kernel the harness may slow: another boot ID, CAP_SYS_TIME held.
func guest() *fakeKernel { return &fakeKernel{bootID: "guest", sysTime: true, rate: Nominal} }

const harness = "harness"

func TestForPPMSplitsTickAndFrequency(t *testing.T) {
	for ppm, want := range map[int]Rate{
		0:       Nominal,
		-5000:   {Tick: 9950},
		-9900:   {Tick: 9901},
		-11000:  {Tick: 9890},
		20000:   {Tick: 10200},
		300:     {Tick: 10003},
		-5050:   {Tick: 9950, Freq: -50 << 16},
		99:      {Tick: 10000, Freq: 99 << 16},
		-1:      {Tick: 10000, Freq: -1 << 16},
		MaxPPM:  {Tick: 11000},
		-MaxPPM: {Tick: 9000},
		12345:   {Tick: 10123, Freq: 45 << 16},
		-12345:  {Tick: 9877, Freq: -45 << 16},
	} {
		got, err := ForPPM(ppm)
		if err != nil {
			t.Fatalf("ForPPM(%d): %v", ppm, err)
		}
		if got != want {
			t.Errorf("ForPPM(%d) = %+v, want %+v", ppm, got, want)
		}
		if got.PPM() != float64(ppm) {
			t.Errorf("ForPPM(%d).PPM() = %v", ppm, got.PPM())
		}
		// The frequency offset stays inside the kernel's ±500 ppm.
		if f := got.Freq / freqScale; f < -500 || f > 500 {
			t.Errorf("ForPPM(%d) freq %d ppm out of range", ppm, f)
		}
	}
}

func TestForPPMRefusesOutOfRange(t *testing.T) {
	for _, ppm := range []int{MaxPPM + 1, -MaxPPM - 1, 1 << 30} {
		_, err := ForPPM(ppm)
		var ue *UsageError
		if !errors.As(err, &ue) || !errors.Is(err, ErrUsage) {
			t.Errorf("ForPPM(%d) = %v, want *UsageError", ppm, err)
		}
	}
}

func TestRateString(t *testing.T) {
	for r, want := range map[Rate]string{
		Nominal:                       "tick 10000 freq 0.000 ppm (0.000 ppm)",
		{Tick: 9950, Freq: -50 << 16}: "tick 9950 freq -50.000 ppm (-5050.000 ppm)",
		{Tick: 10000, Freq: 1 << 15}:  "tick 10000 freq 0.500 ppm (0.500 ppm)",
	} {
		if got := r.String(); got != want {
			t.Errorf("%+v.String() = %q, want %q", r, got, want)
		}
	}
}

func TestCheckRefusals(t *testing.T) {
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		k       *fakeKernel
		harness string
		want    error
	}{
		"guest":              {guest(), harness, nil},
		"no harness id":      {guest(), "", ErrUsage},
		"harness kernel":     {&fakeKernel{bootID: harness, sysTime: true}, harness, ErrHarnessKernel},
		"no CAP_SYS_TIME":    {&fakeKernel{bootID: "guest"}, harness, ErrNoSysTime},
		"boot id unreadable": {&fakeKernel{bootErr: boom, sysTime: true}, harness, boom},
		"capget fails":       {&fakeKernel{bootID: "guest", capErr: boom}, harness, boom},
		// The harness check comes first: a harness kernel is refused even
		// when the capability is missing too.
		"harness kernel without the capability": {&fakeKernel{bootID: harness}, harness, ErrHarnessKernel},
	} {
		err := Check(tc.k, tc.harness)
		if tc.want == nil {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	var ke *KernelError
	if err := Check(&fakeKernel{bootErr: boom}, harness); !errors.As(err, &ke) || ke.Op != "boot id" {
		t.Fatalf("err = %v, want *KernelError for the boot id", err)
	}
}

func TestSetReportsKernelFailures(t *testing.T) {
	boom := errors.New("boom")
	k := guest()
	k.readErr = boom
	var ke *KernelError
	if _, err := Set(k, Nominal, harness); !errors.As(err, &ke) || ke.Op != "adjtimex read" || len(k.sets) != 0 {
		t.Fatalf("read failure: %v, sets %+v", err, k.sets)
	}
	k = guest()
	k.setErr = boom
	prev, err := Set(k, Rate{Tick: 9950}, harness)
	if !errors.As(err, &ke) || ke.Op != "adjtimex set" || !errors.Is(err, boom) || prev != Nominal {
		t.Fatalf("set failure: %+v, %v", prev, err)
	}
}
