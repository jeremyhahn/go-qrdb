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

// Package peertls reads the client-side TLS state of the connection a gRPC
// call was answered on, for the gRPC and Unix transports' tenant barrier
// init proof.
package peertls

import (
	"crypto/tls"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// State returns the TLS state of the connection p was filled from by
// grpc.Peer, or nil when that connection was not TLS.
func State(p *peer.Peer) *tls.ConnectionState {
	if p == nil {
		return nil
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil
	}
	return &info.State
}
