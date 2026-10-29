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
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// StorageFactory opens the storage.Storage behind a raft-free (standalone)
// node's data plane. dataDir is the node's install directory; dataPlaneFS, when
// non-nil, is the seal barrier's encrypting file system, which a factory that
// writes files must route its I/O through so the data stays under the barrier.
// The factory owns the storage's construction; the node closes it on stop.
//
// The seal store is deliberately not behind this factory: per-node seal keys
// are the one node-local state the architecture allows, so the seal store is
// opened by the node itself (see docs/seal).
type StorageFactory func(dataDir string, dataPlaneFS filesys.FS) (storage.Storage, error)

// seamOptions holds the composition seams a composer fills to substitute an
// enhanced implementation (go-xkms hardware, go-quicbaas managed custody) for
// a software default. It is embedded in serverOptions; every field is set only
// by its With* option below, and nil keeps the software default.
//
// Like the crypto-injection options, these are read by the embedders through
// ResolveCryptoOptions and forwarded into lifecycle.Infra, because the
// services they configure are built before server.New runs.
type seamOptions struct {
	backupSink              service.BackupSink
	clock                   clock.Clock
	storageFactory          StorageFactory
	dekProvider             dek.Provider
	headSignerProvider      vlog.HeadSignerProvider
	invitationRedeemLimiter ratelimit.Limiter
	holderActLimiter        ratelimit.Limiter
	webTransportPath        string
	webTransportHandler     rest.WebTransportHandler
}

// WithBackupSink sets the durable sink backup artifacts are persisted to. Nil
// means no sink: backup artifacts are held in memory only and are lost on
// restart. qrdbd passes a file-backed sink only when --backup-dir is set; its
// default is empty. The sink's owner closes it.
func WithBackupSink(sink service.BackupSink) ServerOption {
	return func(o *serverOptions) { o.backupSink = sink }
}

// WithClock sets the time source of the node's security time gates: lease
// expiry, key-delivery freshness and replay window, invitation expiry and the
// token buckets behind the redeem and holder-act limiters. Nil keeps the real
// clock.
func WithClock(c clock.Clock) ServerOption {
	return func(o *serverOptions) { o.clock = c }
}

// WithStorageFactory sets the factory that opens a standalone node's data-plane
// storage in place of the configured backend (memory, file, objstore, pebble).
// Nil keeps the configured backend.
func WithStorageFactory(f StorageFactory) ServerOption {
	return func(o *serverOptions) { o.storageFactory = f }
}

// WithDEKProvider sets the engine behind the tenant DEK service in place of the
// software dek.Keyring, so a device that holds the tenant keys without
// exporting them can seal and open the data. Nil keeps the keyring, wrapped
// through WithKeyWrapRegistry / WithKeyWrapProvider. The provider's owner
// closes it.
func WithDEKProvider(p dek.Provider) ServerOption {
	return func(o *serverOptions) { o.dekProvider = p }
}

// WithHeadSignerProvider sets the provider that creates, rotates and signs
// with each verifiable log's head-signing key, in place of the software
// provider, so a device that holds the keys without exporting them signs the
// log heads. Such a provider writes no k/<version> key record: the log's
// descriptor records its key reference and public key. Nil keeps the software
// provider, which keeps each log's key in its sealed k/<version> record. The
// provider's owner closes it.
func WithHeadSignerProvider(p vlog.HeadSignerProvider) ServerOption {
	return func(o *serverOptions) { o.headSignerProvider = p }
}

// WithInvitationRedeemLimiter sets the limiter that throttles invitation
// redemption, in place of the node's built-in token bucket. It is asked once
// per source address and once per tenant, each under its own key prefix and the
// policy auth.invitation_redeem_limit resolves. Nil keeps the built-in bucket.
// The limiter's owner closes it.
func WithInvitationRedeemLimiter(l ratelimit.Limiter) ServerOption {
	return func(o *serverOptions) { o.invitationRedeemLimiter = l }
}

// WithWebTransportHandler serves WebTransport sessions
// (draft-ietf-webtrans-http3-16) on path of the REST HTTP/3 listener, handing
// each to h after the REST authentication and tenant-binding chain has
// admitted its CONNECT (rest.WithWebTransportHandler). Without it the listener
// advertises no WebTransport SETTINGS.
func WithWebTransportHandler(path string, h rest.WebTransportHandler) ServerOption {
	return func(o *serverOptions) {
		o.webTransportPath = path
		o.webTransportHandler = h
	}
}

// WithHolderActLimiter sets the limiter that throttles share-holder acts (the
// ceremony's quorum challenges and the embedded transport's anonymous holder
// claims), in place of the process-wide token bucket. Nil keeps the built-in
// bucket. The limiter's owner closes it.
func WithHolderActLimiter(l ratelimit.Limiter) ServerOption {
	return func(o *serverOptions) { o.holderActLimiter = l }
}
