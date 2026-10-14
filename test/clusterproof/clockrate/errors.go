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

import "errors"

var (
	// ErrNoSysTime reports a process without CAP_SYS_TIME in its effective
	// set: adjtimex(2) would refuse to change the rate.
	ErrNoSysTime = errors.New("clockrate: CAP_SYS_TIME is not in the effective set")
	// ErrHarnessKernel reports a target that runs on the harness's own
	// kernel: changing its rate would change the harness's clocks too.
	ErrHarnessKernel = errors.New("clockrate: the target runs on the harness's kernel")
	// ErrUsage reports a malformed clock-rate request.
	ErrUsage = errors.New("clockrate: usage")
	// ErrUnsupported reports a platform without adjtimex(2).
	ErrUnsupported = errors.New("clockrate: adjtimex is Linux-only")
)

// UsageError reports a malformed clock-rate request. It wraps ErrUsage.
type UsageError struct {
	Reason string
}

func (e *UsageError) Error() string { return "clockrate: " + e.Reason }

func (e *UsageError) Unwrap() error { return ErrUsage }

// KernelError reports a kernel call that failed.
type KernelError struct {
	Op  string
	Err error
}

func (e *KernelError) Error() string { return "clockrate: " + e.Op + ": " + e.Err.Error() }

func (e *KernelError) Unwrap() error { return e.Err }
