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

package seal

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialKind_String(t *testing.T) {
	tests := map[CredentialKind]string{
		CredentialNone:       "none",
		CredentialPassphrase: "passphrase",
		CredentialShares:     "shares",
		CredentialKind(99):   "unknown",
	}
	for kind, want := range tests {
		require.Equal(t, want, kind.String())
	}
}

func TestCustody_String(t *testing.T) {
	tests := map[Custody]string{
		CustodyNone:     "none",
		CustodyOwner:    "owner",
		CustodyPlatform: "platform",
		Custody(99):     "unknown",
	}
	for custody, want := range tests {
		require.Equal(t, want, custody.String())
	}
}

// TestNewBarrierKeyProvider_NilRegistryUsesDefault resolves the software
// strategies the default registry carries, reporting each one's terms.
func TestNewBarrierKeyProvider_NilRegistryUsesDefault(t *testing.T) {
	provider := NewBarrierKeyProvider(nil)

	tests := []struct {
		id         StrategyID
		credential CredentialKind
		custody    Custody
	}{
		{StrategyPassphrase, CredentialPassphrase, CustodyOwner},
		{StrategyNone, CredentialNone, CustodyNone},
	}
	for _, tt := range tests {
		t.Run(string(tt.id), func(t *testing.T) {
			got, err := provider.BarrierStrategy(context.Background(), BarrierKeyRequest{
				Barrier:    "tenant/acme",
				NodeID:     "local",
				StrategyID: tt.id,
			})
			require.NoError(t, err)
			require.NotNil(t, got.Strategy)
			require.Equal(t, tt.credential, got.Credential)
			require.Equal(t, tt.custody, got.Custody)
		})
	}
}

// TestBarrierKeyProvider_RegisteredStrategyGetsPlatformTerms covers a
// strategy go-qrdb does not know: the provider returns the registry's
// instance and never claims owner custody it cannot see.
func TestBarrierKeyProvider_RegisteredStrategyGetsPlatformTerms(t *testing.T) {
	const deviceHeld StrategyID = "device-held"
	want := NewNoneStrategy()
	reg := NewStrategyRegistry()
	require.NoError(t, reg.Register(deviceHeld, func() (SealingStrategy, error) {
		return want, nil
	}))

	got, err := NewBarrierKeyProvider(reg).BarrierStrategy(context.Background(), BarrierKeyRequest{StrategyID: deviceHeld})
	require.NoError(t, err)
	require.Same(t, want, got.Strategy)
	require.Equal(t, CredentialNone, got.Credential)
	require.Equal(t, CustodyPlatform, got.Custody)
}

func TestBarrierKeyProvider_UnregisteredStrategy(t *testing.T) {
	_, err := NewBarrierKeyProvider(NewStrategyRegistry()).BarrierStrategy(context.Background(), BarrierKeyRequest{StrategyID: StrategyPassphrase})

	var notRegistered *StrategyNotRegisteredError
	require.ErrorAs(t, err, &notRegistered)
}

func TestBarrierKeyProvider_FactoryError(t *testing.T) {
	const failing StrategyID = "failing"
	errFactory := errors.New("factory refused")
	reg := NewStrategyRegistry()
	require.NoError(t, reg.Register(failing, func() (SealingStrategy, error) {
		return nil, errFactory
	}))

	got, err := NewBarrierKeyProvider(reg).BarrierStrategy(context.Background(), BarrierKeyRequest{StrategyID: failing})
	require.ErrorIs(t, err, errFactory)
	require.Nil(t, got.Strategy)
}
