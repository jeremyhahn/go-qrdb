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

// Compile-time interface check.
var _ ThresholdInitializer = (*NoopThresholdInitializer)(nil)

// NoopThresholdInitializer is a placeholder implementation that reports
// itself as unavailable and returns ErrThresholdNotSupported for all
// threshold operations. It is useful as a default or fallback.
type NoopThresholdInitializer struct{}

// VendorName returns "noop".
func (n *NoopThresholdInitializer) VendorName() string {
	return "noop"
}

// Available always returns false, indicating that no hardware is present.
func (n *NoopThresholdInitializer) Available() bool {
	return false
}

// InitializeThreshold returns ErrThresholdNotSupported.
func (n *NoopThresholdInitializer) InitializeThreshold(_ context.Context, _ *ThresholdOptions) error {
	return ErrThresholdNotSupported
}

// ImportShare returns ErrThresholdNotSupported.
func (n *NoopThresholdInitializer) ImportShare(_ context.Context, _ []byte) error {
	return ErrThresholdNotSupported
}

// ThresholdStatus returns ErrThresholdNotSupported.
func (n *NoopThresholdInitializer) ThresholdStatus(_ context.Context) (*ThresholdStatus, error) {
	return nil, ErrThresholdNotSupported
}
