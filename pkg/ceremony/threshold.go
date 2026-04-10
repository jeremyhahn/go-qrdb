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

package ceremony

import "context"

// ThresholdOptions configures a threshold initialization operation
// on a hardware security module or software threshold implementation.
type ThresholdOptions struct {
	// Threshold is the minimum number of shares (M) required to
	// reconstruct the secret.
	Threshold int

	// Total is the total number of shares (N) to generate.
	Total int

	// SOPIN is the Security Officer PIN for authenticating to the device.
	SOPIN string
}

// ThresholdStatus reports the current state of a threshold operation.
type ThresholdStatus struct {
	// Threshold is the required number of shares.
	Threshold int

	// Total is the total number of shares.
	Total int

	// Imported is the number of shares imported so far.
	Imported int

	// Ready indicates whether the threshold has been met and the
	// device is ready for operations.
	Ready bool
}

// ThresholdInitializer is the interface for hardware or software
// implementations that support threshold-based secret sharing natively.
type ThresholdInitializer interface {
	// VendorName returns the name of the threshold implementation vendor.
	VendorName() string

	// Available reports whether the vendor's hardware or software is
	// present and accessible.
	Available() bool

	// InitializeThreshold sets up the threshold configuration on the device.
	InitializeThreshold(ctx context.Context, opts *ThresholdOptions) error

	// ImportShare imports a single key share into the device.
	ImportShare(ctx context.Context, share []byte) error

	// ThresholdStatus returns the current threshold operation status.
	ThresholdStatus(ctx context.Context) (*ThresholdStatus, error)
}
