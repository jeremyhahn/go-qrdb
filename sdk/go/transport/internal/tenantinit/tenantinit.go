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

// Package tenantinit drives a tenant barrier init over any transport's wire:
// which requests it takes and which statement each carries. Every transport
// issues the requests its own way and runs this one flow over them. It is
// internal: only the transport implementations may import it, and it lives
// apart from package transport because pkg/barrierinit imports that package.
package tenantinit

import (
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Round is what one init/complete request on a transport's wire carries
// beyond the transport.TenantBarrierInitRequest itself.
type Round struct {
	// Statement is the owner's signed init statement, nil without a Signer.
	Statement []byte

	// Complete marks a Shamir init's second request, which carries no
	// officers or policy and echoes Roster and UnsealKeyCheck, the split the
	// first request prepared, for the node to complete.
	Complete       bool
	Roster         []byte
	UnsealKeyCheck []byte
}

// Run drives req over send, which issues one init/complete request -- its own
// init/begin and proof included -- on a transport's wire. Every strategy but
// Shamir is one request carrying the statement barrierinit.SignTenantInit
// signs. A Shamir init is two: the first prepares the split and returns its
// roster and unseal key check value, which req.Signer signs into the
// statement (barrierinit.SignTenantShamirInit); the second carries that
// statement and echoes the split, and the node keys the barrier under it.
// The result is the second's, with the holder certificates the first issued.
// A second response reporting AdminPending is a committed init like any
// other: its holders are the certificates that unseal the remaining nodes,
// and a retried init would answer already-initialized without them.
func Run(req *transport.TenantBarrierInitRequest, send func(Round) (*transport.TenantBarrierInitResponse, error)) (*transport.TenantBarrierInitResponse, error) {
	if req.Strategy != string(seal.StrategyShamir) {
		statement, err := barrierinit.SignTenantInit(req.Signer, req.TenantID, req.Strategy, time.Now())
		if err != nil {
			return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
		}
		return send(Round{Statement: statement})
	}
	first, err := send(Round{})
	if err != nil {
		return nil, err
	}
	if len(first.UnsealKeyCheck) == 0 || len(first.Roster) == 0 {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: &transport.ShamirSplitMissingError{TenantID: req.TenantID}}
	}
	statement, err := barrierinit.SignTenantShamirInit(req.Signer, req.TenantID, first.Threshold, first.Roster, first.UnsealKeyCheck, time.Now())
	if err != nil {
		return nil, &transport.RequestError{Method: "TenantBarrierInit", Err: err}
	}
	second, err := send(Round{
		Statement:      statement,
		Complete:       true,
		Roster:         first.Roster,
		UnsealKeyCheck: first.UnsealKeyCheck,
	})
	if err != nil {
		return nil, err
	}
	second.Holders = first.Holders
	return second, nil
}
