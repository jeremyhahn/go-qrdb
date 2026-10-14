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

package seal

import "context"

// BarrierKeyProvider is the seam through which one node obtains the
// SealingStrategy that seals its own key for one barrier: the node barrier, a
// tenant barrier or a database barrier. go-qrdb ships only the software
// implementation over a StrategyRegistry (NewBarrierKeyProvider); a composer
// injects hardware- or KMS-backed implementations (go-xkms) through
// WithBarrierKeyProvider. go-qrdb carries no HSM, TPM or KMS code of its own.
// Implementations must be safe for concurrent use.
type BarrierKeyProvider interface {
	// BarrierStrategy returns the strategy that seals this node's key for
	// req.Barrier, what unsealing it needs from a client, and who holds the
	// wrapping key.
	BarrierStrategy(ctx context.Context, req BarrierKeyRequest) (BarrierKeyStrategy, error)
}

// BarrierKeyRequest names one node's key for one barrier.
type BarrierKeyRequest struct {
	// Barrier is the barrier's cluster-wide name: "node", "tenant/<tenant>"
	// or "database/<tenant>/<database>" (the clusterseal.Barrier grammar).
	Barrier string
	// NodeID is the node whose key this is. Every node seals its own key.
	NodeID string
	// StrategyID is the strategy the barrier's owner chose.
	StrategyID StrategyID
	// KeyRef is the owner's opaque reference to the key that wraps this
	// node's key: one key the owner controls, or a per-node template the
	// provider resolves. go-qrdb never interprets it. Empty for the software
	// strategies.
	KeyRef string
	// Epoch is the credential epoch: 1 at init, one more per credential
	// change.
	Epoch uint64
}

// BarrierKeyStrategy is a resolved strategy and what it needs.
type BarrierKeyStrategy struct {
	// Strategy seals and unseals this node's key.
	Strategy SealingStrategy
	// Credential is what a client must supply to initialize and unseal.
	Credential CredentialKind
	// Custody is who can release the key without the owner's credential.
	Custody Custody
}

// CredentialKind is what a client must supply to initialize or unseal a
// barrier key.
type CredentialKind int

const (
	// CredentialNone needs nothing from a client: the strategy's own
	// wrapping key releases the barrier key.
	CredentialNone CredentialKind = iota
	// CredentialPassphrase needs the owner's passphrase.
	CredentialPassphrase
	// CredentialShares needs a quorum of the owner's Shamir share holders.
	CredentialShares
)

var credentialKindNames = map[CredentialKind]string{
	CredentialNone:       "none",
	CredentialPassphrase: "passphrase",
	CredentialShares:     "shares",
}

// String returns the credential kind's name, or "unknown".
func (k CredentialKind) String() string {
	if name, ok := credentialKindNames[k]; ok {
		return name
	}
	return "unknown"
}

// Custody records who can release a barrier key, and so who can unseal the
// barrier without the owner present. It states the owner's own choice: an
// owner that opts into auto-unseal hands custody to the platform, which is the
// owner's security posture to choose. A provider declares the custody of what
// it returns; go-qrdb cannot inspect a KMS key's policy, so the composer that
// injects the provider vouches for the declaration.
type Custody int

const (
	// CustodyNone means the key is not protected: the barrier is plaintext.
	CustodyNone Custody = iota
	// CustodyOwner means only the barrier's owner can release the key: its
	// passphrase, its share holders, or a key the owner controls.
	CustodyOwner
	// CustodyPlatform means the platform can release the key unattended:
	// the owner chose auto-unseal under a key the platform holds.
	CustodyPlatform
)

var custodyNames = map[Custody]string{
	CustodyNone:     "none",
	CustodyOwner:    "owner",
	CustodyPlatform: "platform",
}

// String returns the custody's name, or "unknown".
func (c Custody) String() string {
	if name, ok := custodyNames[c]; ok {
		return name
	}
	return "unknown"
}

// strategyTerms is what a built-in strategy needs and who holds its key.
type strategyTerms struct {
	credential CredentialKind
	custody    Custody
}

// builtinStrategyTerms maps the strategies go-qrdb knows to their terms.
var builtinStrategyTerms = map[StrategyID]strategyTerms{
	StrategyPassphrase:    {CredentialPassphrase, CustodyOwner},
	StrategyShamir:        {CredentialShares, CustodyOwner},
	StrategySystemBarrier: {CredentialNone, CustodyPlatform},
	StrategyNone:          {CredentialNone, CustodyNone},
}

// registryTerms is what the default provider reports for a strategy it does
// not know: one registered by name, typically a device-held key on the node.
// It never claims owner custody it cannot see; a composer whose strategy wraps
// under a key the owner controls injects its own provider to say so.
var registryTerms = strategyTerms{CredentialNone, CustodyPlatform}

// registryBarrierKeyProvider is the default BarrierKeyProvider: it resolves a
// strategy by name from a StrategyRegistry.
type registryBarrierKeyProvider struct {
	registry StrategyRegistry
}

// NewBarrierKeyProvider returns the default BarrierKeyProvider, which
// resolves req.StrategyID from reg and ignores KeyRef, NodeID and Epoch: a
// strategy built by a parameterless factory has nothing to bind them to. A nil
// reg is NewDefaultStrategyRegistry. An unregistered strategy is refused with
// *StrategyNotRegisteredError.
func NewBarrierKeyProvider(reg StrategyRegistry) BarrierKeyProvider {
	if reg == nil {
		reg = NewDefaultStrategyRegistry()
	}
	return &registryBarrierKeyProvider{registry: reg}
}

// BarrierStrategy implements BarrierKeyProvider.
func (p *registryBarrierKeyProvider) BarrierStrategy(_ context.Context, req BarrierKeyRequest) (BarrierKeyStrategy, error) {
	factory, err := p.registry.LookupFactory(req.StrategyID)
	if err != nil {
		return BarrierKeyStrategy{}, err
	}
	strategy, err := factory()
	if err != nil {
		return BarrierKeyStrategy{}, err
	}
	terms, ok := builtinStrategyTerms[req.StrategyID]
	if !ok {
		terms = registryTerms
	}
	return BarrierKeyStrategy{Strategy: strategy, Credential: terms.credential, Custody: terms.custody}, nil
}
