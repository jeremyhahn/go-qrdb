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

package auth

import "strconv"

// ChainPosition is where an embedder-supplied authenticator sits in a node's
// one authentication chain, relative to the built-in mTLS authenticator.
//
// The chain is first-success-wins on every transport: the first authenticator
// that returns an identity decides the request and nothing after it runs. The
// position is therefore a statement about which credential decides a request
// that presents more than one -- a client certificate AND a bearer, say.
//
// The zero value is not a position. Every placement names one explicitly, and
// AuthenticatorChain refuses one that does not with *InvalidChainPositionError,
// so an embedder can never get an ordering it did not ask for.
type ChainPosition int

const (
	// ChainBeforeMTLS runs the authenticator ahead of mTLS: it decides every
	// request it accepts, including one that also presents a valid client
	// certificate. A request it declines falls through to mTLS.
	ChainBeforeMTLS ChainPosition = iota + 1

	// ChainAfterMTLS runs the authenticator after mTLS: a request that
	// presents a valid client certificate is decided by mTLS, and only one
	// mTLS declines reaches the authenticator.
	ChainAfterMTLS

	// ChainReplaceMTLS removes the mTLS authenticator from the chain and puts
	// the authenticator in its place. A client certificate then authenticates
	// nobody by itself.
	//
	// It removes only the mTLS AUTHENTICATOR's decision. Each transport's TLS
	// is untouched: the listener still terminates TLS, still verifies any
	// client certificate presented against its CA pool, and the verified
	// connection state still reaches the chain (TLSStateFromContext), where a
	// replacing authenticator may read it -- to bind a bearer to the
	// certificate that carried it, for instance. The on-behalf forwarding path
	// between cluster nodes is not part of this chain and is unaffected.
	ChainReplaceMTLS
)

// chainPositionNames is the spelling of every valid ChainPosition, and the
// set AuthenticatorChain validates a placement against.
var chainPositionNames = map[ChainPosition]string{
	ChainBeforeMTLS:  "before-mtls",
	ChainAfterMTLS:   "after-mtls",
	ChainReplaceMTLS: "replace-mtls",
}

// String returns the position's spelling, or "chain-position(N)" for a value
// that is not one.
func (p ChainPosition) String() string {
	if name, ok := chainPositionNames[p]; ok {
		return name
	}
	return "chain-position(" + strconv.Itoa(int(p)) + ")"
}

// AuthenticatorPlacement is one embedder-supplied authenticator and the
// position it takes in the chain.
type AuthenticatorPlacement struct {
	Position      ChainPosition
	Authenticator Authenticator
}

// AuthenticatorChain assembles a node's one ordered authenticator chain from
// the built-in mTLS authenticator and the embedder's placements:
//
//	ChainBeforeMTLS placements, in order
//	mtls -- or, when any placement is ChainReplaceMTLS, those placements in order
//	ChainAfterMTLS placements, in order
//
// A placement whose Authenticator is nil installs nothing, but its position is
// still validated and still counts: a ChainReplaceMTLS placement removes mTLS
// whether or not it carries an authenticator, so a chain that asked for mTLS
// to be replaced never quietly keeps it.
//
// It returns *InvalidChainPositionError for a placement whose position is not
// one of the three, and *EmptyAuthenticatorChainError when mTLS was replaced
// and nothing took its place anywhere in the chain: a chain no request can
// pass is a wiring bug, not a posture.
func AuthenticatorChain(mtls Authenticator, placements []AuthenticatorPlacement) ([]Authenticator, error) {
	byPosition := make(map[ChainPosition][]Authenticator, len(chainPositionNames))
	replaced := false
	for i, p := range placements {
		if _, ok := chainPositionNames[p.Position]; !ok {
			return nil, &InvalidChainPositionError{Index: i, Position: p.Position}
		}
		replaced = replaced || p.Position == ChainReplaceMTLS
		if p.Authenticator != nil {
			byPosition[p.Position] = append(byPosition[p.Position], p.Authenticator)
		}
	}

	middle := []Authenticator{mtls}
	if replaced {
		middle = byPosition[ChainReplaceMTLS]
	}
	chain := make([]Authenticator, 0, len(placements)+1)
	chain = append(chain, byPosition[ChainBeforeMTLS]...)
	chain = append(chain, middle...)
	chain = append(chain, byPosition[ChainAfterMTLS]...)
	if len(chain) == 0 {
		return nil, &EmptyAuthenticatorChainError{}
	}
	return chain, nil
}

// InvalidChainPositionError reports a placement whose position is not
// ChainBeforeMTLS, ChainAfterMTLS or ChainReplaceMTLS -- most often the zero
// value, from a placement that never chose one.
type InvalidChainPositionError struct {
	// Index is the placement's index in the list AuthenticatorChain was given.
	Index    int
	Position ChainPosition
}

// Error names the offending placement and its position.
func (e *InvalidChainPositionError) Error() string {
	return "qrdb/auth: authenticator placement " + strconv.Itoa(e.Index) +
		" has invalid chain position " + e.Position.String() +
		` (want "before-mtls", "after-mtls" or "replace-mtls")`
}

// EmptyAuthenticatorChainError reports a chain whose mTLS authenticator was
// replaced (ChainReplaceMTLS) with no authenticator anywhere in the chain to
// take its place.
type EmptyAuthenticatorChainError struct{}

// Error describes the empty chain.
func (e *EmptyAuthenticatorChainError) Error() string {
	return "qrdb/auth: mtls authenticator replaced but the chain holds no authenticator"
}
