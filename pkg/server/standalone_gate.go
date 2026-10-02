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

package server

import (
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// StandaloneModeHint is the remedy text carried by every raft-only API
// refusal under mode "standalone".
const StandaloneModeHint = capabilities.RaftOnlyHint

// notImplemented is the refusal a registry resolution answers when this node
// cannot serve the named service — because it was composed without it, or
// because the mode gates it (hint names the remedy, e.g. StandaloneModeHint).
//
// It is a SERVICE-layer error, deliberately. This registry is handed to every
// transport, and the refusal used to be *rest.ServiceNotImplementedError — a
// type only the REST transport classified, so REST answered 501 while gRPC,
// unix, QUIC and MCP fell through to their unclassified default and told a
// caller its server had BROKEN for a condition that is a deployment mode
// (go-qrdb#473). service.CapabilityUnsupportedError is the vocabulary every
// transport's classifier already maps to "this node cannot do that": 501,
// codes.Unimplemented, wire.StatusUnimplemented, CodeUnimplemented — and the
// SDK rebuilds transport.CapabilityUnsupportedError from each. Returning it
// here fixes all five transports at once, and removes the server package's
// reach into a transport's error vocabulary.
func notImplemented(svc, hint string) error {
	msg := svc + ": not implemented on this server"
	if hint != "" {
		msg += ": " + hint
	}
	return &service.CapabilityUnsupportedError{Message: msg}
}

// WithStandaloneMode marks the server as running the raft-free standalone
// composition.
//
// Standalone serves the same per-(tenant, db) data plane as raft mode --
// KV, MVCC, watch, lease, lock, transactions and the rest -- through the
// same service registry, so this option no longer narrows the registry.
// What stays raft-only is what is not a property of one database's data:
// cluster membership, rebalance and shard backup (the caller does not pass
// those singletons in this mode), and the etcd compatibility listener,
// which startEtcd refuses under this option with StandaloneModeHint. The
// etcd listener is a separate product surface with its own capability flag
// (capabilities.Capabilities.Etcd, raft-only), not one of the data-plane
// families standalone was brought to parity on.
func WithStandaloneMode() ServerOption {
	return func(o *serverOptions) { o.standaloneMode = true }
}
