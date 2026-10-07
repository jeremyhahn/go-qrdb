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

package main

import (
	"context"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// MVCCBinding exposes MVCC-aware key-value operations to the Wails frontend.
type MVCCBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewMVCCBinding creates a new MVCCBinding backed by the given connection.
func NewMVCCBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *MVCCBinding {
	return &MVCCBinding{logger: log, conn: conn}
}

// PutWithLease stores a key-value pair with an attached lease. When the
// lease expires, the key is automatically deleted.
func (b *MVCCBinding) PutWithLease(key string, value string, leaseID int64) (*transport.MVCCKeyValue, error) {
	b.logger.Debug("PutWithLease called", "key", key, "leaseID", leaseID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("PutWithLease failed", "error", err)
		return nil, err
	}
	kv, err := cl.MVCC().PutWithLease(context.Background(), key, []byte(value), leaseID)
	if err != nil {
		b.logger.Error("PutWithLease failed", "key", key, "error", err)
		return nil, err
	}
	b.logger.Debug("PutWithLease completed", "key", key)
	return kv, nil
}

// GetRevision retrieves a key-value pair with full MVCC revision metadata.
func (b *MVCCBinding) GetRevision(key string) (*transport.MVCCKeyValue, error) {
	b.logger.Debug("GetRevision called", "key", key)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetRevision failed", "error", err)
		return nil, err
	}
	kv, err := cl.MVCC().GetRevision(context.Background(), key)
	if err != nil {
		b.logger.Error("GetRevision failed", "key", key, "error", err)
		return nil, err
	}
	b.logger.Debug("GetRevision completed", "key", key)
	return kv, nil
}

// GetAtRevision retrieves a key-value pair at a historical revision.
func (b *MVCCBinding) GetAtRevision(key string, revision int64) (*transport.MVCCKeyValue, error) {
	b.logger.Debug("GetAtRevision called", "key", key, "revision", revision)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetAtRevision failed", "error", err)
		return nil, err
	}
	kv, err := cl.MVCC().GetAtRevision(context.Background(), key, revision)
	if err != nil {
		b.logger.Error("GetAtRevision failed", "key", key, "error", err)
		return nil, err
	}
	b.logger.Debug("GetAtRevision completed", "key", key)
	return kv, nil
}

// Range returns key-value pairs in the half-open interval [start, end)
// with the specified options.
func (b *MVCCBinding) Range(start, end string, opts *transport.RangeOptions) (*transport.RangeResponse, error) {
	b.logger.Debug("Range called", "start", start, "end", end)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Range failed", "error", err)
		return nil, err
	}
	rangeOpts := transport.RangeOptions{}
	if opts != nil {
		rangeOpts = *opts
	}
	resp, err := cl.MVCC().Range(context.Background(), []byte(start), []byte(end), rangeOpts)
	if err != nil {
		b.logger.Error("Range failed", "error", err)
		return nil, err
	}
	b.logger.Debug("Range completed")
	return resp, nil
}

// Compact discards all MVCC revisions older than the specified revision.
func (b *MVCCBinding) Compact(revision int64) error {
	b.logger.Debug("Compact called", "revision", revision)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Compact failed", "error", err)
		return err
	}
	if err := cl.MVCC().Compact(context.Background(), revision); err != nil {
		b.logger.Error("Compact failed", "revision", revision, "error", err)
		return err
	}
	b.logger.Debug("Compact completed", "revision", revision)
	return nil
}

// RestoreToRevision returns a bounded set of keys to the value they held at
// an earlier MVCC revision by proposing new writes -- a logical undo, never
// a Raft rewind. It mirrors TransactionBinding.Execute: a single snake_case
// request object in (req's Prefix/Keys []byte fields decode from the
// base64 strings the TS SDK's wails.ts sends), and the SDK's
// *transport.RestoreRevisionResult returned unconverted, whose
// Changes/Conflicts "key" fields the TS SDK base64-decodes back to
// plaintext for the caller.
func (b *MVCCBinding) RestoreToRevision(req *transport.RestoreRevisionRequest) (*transport.RestoreRevisionResult, error) {
	b.logger.Debug("RestoreToRevision called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RestoreToRevision failed", "error", err)
		return nil, err
	}
	result, err := cl.MVCC().RestoreToRevision(context.Background(), req)
	if err != nil {
		b.logger.Error("RestoreToRevision failed", "error", err)
		return nil, err
	}
	b.logger.Debug("RestoreToRevision completed")
	return result, nil
}

// CurrentRevision returns the latest committed MVCC revision.
func (b *MVCCBinding) CurrentRevision() (int64, error) {
	b.logger.Debug("CurrentRevision called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("CurrentRevision failed", "error", err)
		return 0, err
	}
	rev, err := cl.MVCC().CurrentRevision(context.Background())
	if err != nil {
		b.logger.Error("CurrentRevision failed", "error", err)
		return 0, err
	}
	b.logger.Debug("CurrentRevision completed", "revision", rev)
	return rev, nil
}
