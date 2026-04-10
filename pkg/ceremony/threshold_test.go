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

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- NoopThresholdInitializer tests ---

func TestNoopThresholdInitializer_VendorName(t *testing.T) {
	noop := &NoopThresholdInitializer{}
	assert.Equal(t, "noop", noop.VendorName())
}

func TestNoopThresholdInitializer_Available(t *testing.T) {
	noop := &NoopThresholdInitializer{}
	assert.False(t, noop.Available())
}

func TestNoopThresholdInitializer_InitializeThreshold(t *testing.T) {
	noop := &NoopThresholdInitializer{}
	err := noop.InitializeThreshold(context.Background(), &ThresholdOptions{
		Threshold: 2,
		Total:     3,
		SOPIN:     "test-pin",
	})
	assert.ErrorIs(t, err, ErrThresholdNotSupported)
}

func TestNoopThresholdInitializer_ImportShare(t *testing.T) {
	noop := &NoopThresholdInitializer{}
	err := noop.ImportShare(context.Background(), []byte("share-data"))
	assert.ErrorIs(t, err, ErrThresholdNotSupported)
}

func TestNoopThresholdInitializer_ThresholdStatus(t *testing.T) {
	noop := &NoopThresholdInitializer{}
	status, err := noop.ThresholdStatus(context.Background())
	assert.Nil(t, status)
	assert.ErrorIs(t, err, ErrThresholdNotSupported)
}

// --- ThresholdRegistry tests ---

// availableThresholdInitializer is a test implementation that reports
// itself as available.
type availableThresholdInitializer struct {
	vendor string
}

func (a *availableThresholdInitializer) VendorName() string { return a.vendor }
func (a *availableThresholdInitializer) Available() bool    { return true }
func (a *availableThresholdInitializer) InitializeThreshold(_ context.Context, _ *ThresholdOptions) error {
	return nil
}
func (a *availableThresholdInitializer) ImportShare(_ context.Context, _ []byte) error {
	return nil
}
func (a *availableThresholdInitializer) ThresholdStatus(_ context.Context) (*ThresholdStatus, error) {
	return &ThresholdStatus{Ready: true}, nil
}

func TestThresholdRegistry_RegisterAndGet(t *testing.T) {
	reg := NewThresholdRegistry()

	reg.Register("test-vendor", func() ThresholdInitializer {
		return &availableThresholdInitializer{vendor: "test-vendor"}
	})

	init, err := reg.Get("test-vendor")
	require.NoError(t, err)
	assert.Equal(t, "test-vendor", init.VendorName())
	assert.True(t, init.Available())
}

func TestThresholdRegistry_GetUnregistered(t *testing.T) {
	reg := NewThresholdRegistry()

	_, err := reg.Get("nonexistent")
	assert.ErrorIs(t, err, ErrVendorNotRegistered)
}

func TestThresholdRegistry_GetUnavailableVendor(t *testing.T) {
	reg := NewThresholdRegistry()

	reg.Register("noop", func() ThresholdInitializer {
		return &NoopThresholdInitializer{}
	})

	_, err := reg.Get("noop")
	assert.ErrorIs(t, err, ErrVendorNotAvailable)
}

func TestThresholdRegistry_ListSorted(t *testing.T) {
	reg := NewThresholdRegistry()

	reg.Register("charlie", func() ThresholdInitializer {
		return &availableThresholdInitializer{vendor: "charlie"}
	})
	reg.Register("alice", func() ThresholdInitializer {
		return &availableThresholdInitializer{vendor: "alice"}
	})
	reg.Register("bob", func() ThresholdInitializer {
		return &availableThresholdInitializer{vendor: "bob"}
	})

	vendors := reg.List()
	require.Len(t, vendors, 3)
	assert.Equal(t, []string{"alice", "bob", "charlie"}, vendors)
}

func TestThresholdRegistry_Overwrite(t *testing.T) {
	reg := NewThresholdRegistry()

	reg.Register("vendor-a", func() ThresholdInitializer {
		return &NoopThresholdInitializer{}
	})

	// Overwrite with an available implementation.
	reg.Register("vendor-a", func() ThresholdInitializer {
		return &availableThresholdInitializer{vendor: "vendor-a"}
	})

	init, err := reg.Get("vendor-a")
	require.NoError(t, err)
	assert.Equal(t, "vendor-a", init.VendorName())
	assert.True(t, init.Available())
}

func TestThresholdRegistry_ListEmpty(t *testing.T) {
	reg := NewThresholdRegistry()
	vendors := reg.List()
	assert.Empty(t, vendors)
}
