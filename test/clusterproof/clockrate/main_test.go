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
	"bytes"
	"errors"
	"strings"
	"testing"
)

func runMain(k Kernel, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = Main(args, k, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestMainSetsAndRestoresTheRate(t *testing.T) {
	k := guest()
	code, out, _ := runMain(k, "-not-kernel", harness, "-ppm", "-5050")
	if code != ExitOK || k.rate != (Rate{Tick: 9950, Freq: -50 << 16}) {
		t.Fatalf("exit %d, kernel at %+v", code, k.rate)
	}
	if want := Nominal.String() + " -> " + k.rate.String() + "\n"; out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
	if code, _, _ := runMain(k, "-ppm", "0", "-not-kernel", harness); code != ExitOK || k.rate != Nominal {
		t.Fatalf("restore: exit %d, kernel at %+v", code, k.rate)
	}
}

func TestMainCheck(t *testing.T) {
	k := guest()
	if code, out, _ := runMain(k, "-check", "-not-kernel", harness); code != ExitOK || out != "" || len(k.sets) != 0 {
		t.Fatalf("check: exit %d, stdout %q, sets %+v", code, out, k.sets)
	}
	k = &fakeKernel{bootID: "guest"}
	code, _, errOut := runMain(k, "-check", "-not-kernel", harness)
	if code != ExitNoSysTime || !strings.Contains(errOut, "CAP_SYS_TIME") {
		t.Fatalf("check without the capability: exit %d, stderr %q", code, errOut)
	}
}

func TestMainRefusalsExitWithTheirStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		k    *fakeKernel
		args []string
		want int
	}{
		"harness kernel":    {&fakeKernel{bootID: harness, sysTime: true}, []string{"-not-kernel", harness, "-ppm", "-5000"}, ExitHarnessKernel},
		"no CAP_SYS_TIME":   {&fakeKernel{bootID: "guest"}, []string{"-not-kernel", harness, "-ppm", "-5000"}, ExitNoSysTime},
		"no harness id":     {guest(), []string{"-ppm", "-5000"}, ExitUsage},
		"out of range":      {guest(), []string{"-not-kernel", harness, "-ppm", "100001"}, ExitUsage},
		"neither mode":      {guest(), []string{"-not-kernel", harness}, ExitUsage},
		"both modes":        {guest(), []string{"-not-kernel", harness, "-check", "-ppm", "0"}, ExitUsage},
		"stray argument":    {guest(), []string{"-not-kernel", harness, "-check", "now"}, ExitUsage},
		"unknown flag":      {guest(), []string{"-not-kernel", harness, "-tick", "9950"}, ExitUsage},
		"adjtimex rejected": {&fakeKernel{bootID: "guest", sysTime: true, setErr: errors.New("EINVAL")}, []string{"-not-kernel", harness, "-ppm", "-5000"}, ExitFailed},
	} {
		code, out, _ := runMain(tc.k, tc.args...)
		if code != tc.want {
			t.Errorf("%s: exit %d, want %d", name, code, tc.want)
		}
		if out != "" {
			t.Errorf("%s: stdout %q", name, out)
		}
		if tc.want != ExitFailed && len(tc.k.sets) != 0 {
			t.Errorf("%s: kernel set %+v", name, tc.k.sets)
		}
	}
}

func TestExitCodeAndRefusalRoundTrip(t *testing.T) {
	for _, sentinel := range []error{ErrUsage, ErrNoSysTime, ErrHarnessKernel} {
		code := ExitCode(&KernelError{Op: "wrap", Err: sentinel})
		if got := Refusal(code); got != sentinel {
			t.Errorf("Refusal(ExitCode(%v)) = %v", sentinel, got)
		}
	}
	if ExitCode(nil) != ExitOK || ExitCode(errors.New("boom")) != ExitFailed {
		t.Fatal("ExitCode of nil or an unknown error")
	}
	for _, code := range []int{ExitOK, ExitFailed, 127} {
		if err := Refusal(code); err != nil {
			t.Errorf("Refusal(%d) = %v, want nil", code, err)
		}
	}
}
