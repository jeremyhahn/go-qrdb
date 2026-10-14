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
	"flag"
	"fmt"
	"io"
)

// Exit statuses of Main. The harness reads them back through a target's exec
// template, which carries a remote command's status but not its error.
const (
	ExitOK            = 0
	ExitFailed        = 1
	ExitUsage         = 2
	ExitNoSysTime     = 3
	ExitHarnessKernel = 4
)

// exitCodes maps each refusal to its exit status; any other error is
// ExitFailed.
var exitCodes = map[error]int{
	ErrUsage:         ExitUsage,
	ErrNoSysTime:     ExitNoSysTime,
	ErrHarnessKernel: ExitHarnessKernel,
}

// ExitCode returns the exit status Main reports for err.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	for sentinel, code := range exitCodes {
		if errors.Is(err, sentinel) {
			return code
		}
	}
	return ExitFailed
}

// Refusal returns the error a clock-rate exit status reports, or nil if the
// status is not a refusal.
func Refusal(code int) error {
	for sentinel, c := range exitCodes {
		if c == code {
			return sentinel
		}
	}
	return nil
}

// Main runs the clock-rate command on k:
//
//	clock-rate -not-kernel <boot-id> -check
//	clock-rate -not-kernel <boot-id> -ppm <n>
//
// -check exits 0 if Check passes; -ppm sets the rate ForPPM returns, 0
// restoring Nominal, and prints the rate replaced and the rate set. The exit
// status is ExitCode of the error.
func Main(args []string, k Kernel, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clock-rate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "only check that the rate may be changed")
	ppm := fs.Int("ppm", 0, "rate change in parts per million; 0 restores the nominal rate")
	harness := fs.String("not-kernel", "", "boot ID of the harness's kernel, which is refused")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	setPPM := false
	fs.Visit(func(f *flag.Flag) { setPPM = setPPM || f.Name == "ppm" })
	if fs.NArg() != 0 || *check == setPPM {
		fmt.Fprintln(stderr, "clock-rate: exactly one of -check and -ppm is required")
		fs.Usage()
		return ExitUsage
	}
	if err := run(k, *check, *ppm, *harness, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return ExitCode(err)
	}
	return ExitOK
}

func run(k Kernel, check bool, ppm int, harness string, stdout io.Writer) error {
	if check {
		return Check(k, harness)
	}
	r, err := ForPPM(ppm)
	if err != nil {
		return err
	}
	prev, err := Set(k, r, harness)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, prev.String(), "->", r.String())
	return nil
}
