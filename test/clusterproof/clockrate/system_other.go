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

//go:build clusterproof && !linux

package clockrate

// System is the Kernel this process runs on. Off Linux every call reports
// ErrUnsupported.
type System struct{}

var _ Kernel = System{}

// BootID reports ErrUnsupported.
func (System) BootID() (string, error) { return "", ErrUnsupported }

// SysTime reports ErrUnsupported.
func (System) SysTime() (bool, error) { return false, ErrUnsupported }

// Rate reports ErrUnsupported.
func (System) Rate() (Rate, error) { return Rate{}, ErrUnsupported }

// SetRate reports ErrUnsupported.
func (System) SetRate(Rate) error { return ErrUnsupported }
