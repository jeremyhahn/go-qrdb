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

// CeremonyDistributor seals a domain key's Shamir shares to that domain's
// share holders. pkg/seal defines the interface (consumer side); pkg/ceremony
// provides the implementations. It is the only way shares leave the barrier:
// no API accepts share bytes as input to a distribution.
//
// A distribution is three calls, in this order, and the barrier makes them:
//
//  1. PrepareRoster snapshots the active holders into a roster. It has no
//     side effects, so the barrier may also call it to validate a rekey
//     before a quorum is gathered.
//  2. Distribute splits the key and seals one share to each roster holder.
//  3. Committed tells the distributor the wrapped key and its roster are
//     durable, so it can record the new share indexes and drop removed
//     holders.
//
// If anything fails between 2 and 3 the previously persisted blob, and with
// it the previous share set, stays in force.
type CeremonyDistributor interface {
	// PrepareRoster returns a roster of every active holder of the domain
	// except those named in exclude, with share indexes 1..N assigned, the
	// given policy and threshold. It returns an error when the holder set
	// cannot carry the threshold.
	PrepareRoster(ctx context.Context, domain QuorumDomain, threshold int, policy FactorPolicy, exclude []string) (*HolderRoster, error)

	// Distribute splits secret into roster.Total shares with roster.Threshold
	// and seals the share with index i to the roster holder whose ShareIndex
	// is i, binding rosterDigest into each sealed share. Every share from any
	// earlier distribution of the domain is destroyed first. secret is wiped
	// before Distribute returns, whatever the outcome. It returns the number
	// of shares sealed.
	Distribute(ctx context.Context, secret []byte, roster *HolderRoster, rosterDigest []byte) (int, error)

	// Committed records that roster is now the domain's durable roster and
	// that the holders in removed are no longer holders.
	Committed(ctx context.Context, roster *HolderRoster, removed []string) error
}

// HolderVerifier authenticates the holder behind a share submission. pkg/seal
// defines it and counts no share without it; pkg/ceremony implements it over
// the holder's enrolled authentication factors. It must work while the
// barrier is sealed, so it may rely on nothing but the roster it is handed
// and state it holds in memory.
type HolderVerifier interface {
	// VerifyHolder checks proof authenticates holder for op in domain against
	// the roster whose digest is rosterDigest. The challenge the proof answers
	// must have been issued for exactly that (domain, op, holder, digest)
	// and is consumed by the call, successful or not.
	VerifyHolder(ctx context.Context, domain QuorumDomain, op QuorumOp, holder string, roster *HolderRoster, rosterDigest []byte, proof HolderProof) error
}

// HolderProof is the authentication material that accompanies a share.
type HolderProof struct {
	// ChallengeID names the challenge the proof answers.
	ChallengeID string

	// Factor names the factor that produced Response. Empty selects the
	// holder's designated factor.
	Factor string

	// Response is the factor response (a signature or an assertion).
	Response []byte

	// ResponseMetadata carries the factor's response attributes.
	ResponseMetadata map[string]string

	// PeerCertThumbprint is the thumbprint of the client certificate the
	// caller presented on a verified TLS session, set by the transport from
	// the connection and never from the request body. Empty when the
	// transport carried no verified client certificate.
	PeerCertThumbprint string
}
