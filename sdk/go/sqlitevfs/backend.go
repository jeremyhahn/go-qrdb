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

package sqlitevfs

import (
	"context"
	"fmt"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// NewBackend returns the SQLite page store's Backend over a Go SDK client's
// MVCC and transaction services. A nil client is refused with
// sqlitevfs.ErrNilBackend.
func NewBackend(mvcc transport.MVCCClient, txn transport.TransactionClient) (*sqlitevfs.ServiceBackend, error) {
	if mvcc == nil || txn == nil {
		return nil, sqlitevfs.ErrNilBackend
	}
	return sqlitevfs.NewServiceBackend(kvAdapter{c: mvcc}, txnAdapter{c: txn})
}

// kvAdapter is a transport.MVCCClient as a sqlitevfs.ServiceKV.
type kvAdapter struct {
	c transport.MVCCClient
}

// GetAtRevision reads key at revision as a one-key range [key, key+"\x00"),
// whose bounds the gRPC, Unix and QUIC transports carry as bytes, so a page key that is not
// valid UTF-8 reads back exactly. An absent key is the server's not-found
// error, so the backend reads it as not found.
func (a kvAdapter) GetAtRevision(ctx context.Context, key string, revision int64) (*service.KeyValue, error) {
	start := []byte(key)
	end := append([]byte(key), 0)
	resp, err := a.c.Range(ctx, start, end, transport.RangeOptions{Revision: revision, Limit: 1})
	if err != nil {
		return nil, readError(err, key, revision)
	}
	if resp == nil || len(resp.Kvs) == 0 || resp.Kvs[0] == nil {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return toKeyValue(resp.Kvs[0]), nil
}

// Range reads [start, end) with the server's range options carried on the
// wire's. MaxResultBytes has no wire field; the backend never sets it.
func (a kvAdapter) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	o := lock.ApplyRangeOptions(opts)
	resp, err := a.c.Range(ctx, start, end, transport.RangeOptions{
		Limit:      o.Limit,
		Revision:   o.Revision,
		CountOnly:  o.CountOnly,
		KeysOnly:   o.KeysOnly,
		SortOrder:  transport.SortOrder(o.SortOrder),
		SortTarget: transport.SortTarget(o.SortTarget),
	})
	if err != nil {
		return nil, 0, 0, wireError(err)
	}
	kvs := make([]*service.KeyValue, 0, len(resp.Kvs))
	for _, k := range resp.Kvs {
		kvs = append(kvs, toKeyValue(k))
	}
	return kvs, resp.Count, resp.Revision, nil
}

// CurrentRevision returns the store's current revision.
func (a kvAdapter) CurrentRevision(ctx context.Context) (int64, error) {
	rev, err := a.c.CurrentRevision(ctx)
	return rev, wireError(err)
}

// MovePrefix moves every key under req.Src to req.Dst in one revision.
func (a kvAdapter) MovePrefix(ctx context.Context, req *service.MovePrefixRequest) (int64, error) {
	ops, err := toWireOps(req.Ops)
	if err != nil {
		return 0, err
	}
	rev, err := a.c.MovePrefix(ctx, &transport.MovePrefixRequest{
		Src:          []byte(req.Src),
		Dst:          []byte(req.Dst),
		ExpectedKeys: req.ExpectedKeys,
		Ops:          ops,
		Compares:     toWireCompares(req.Compares),
	})
	return rev, wireError(err)
}

// txnAdapter is a transport.TransactionClient as a sqlitevfs.ServiceTxn.
type txnAdapter struct {
	c transport.TransactionClient
}

// Execute runs one compare-and-swap transaction. A transaction whose
// compares did not hold is Succeeded=false, not an error.
func (a txnAdapter) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	success, err := toWireOps(successOps)
	if err != nil {
		return nil, err
	}
	failure, err := toWireOps(failureOps)
	if err != nil {
		return nil, err
	}
	resp, err := a.c.ExecuteTransaction(ctx, &transport.TransactionRequest{
		Compares:   toWireCompares(compares),
		SuccessOps: success,
		FailureOps: failure,
	})
	if err != nil {
		return nil, wireError(err)
	}
	out := &service.TransactionResponse{Succeeded: resp.Succeeded, Revision: resp.Revision}
	for _, r := range resp.Responses {
		or := lock.OpResponse{Count: r.Count, Succeeded: r.Succeeded}
		for _, k := range r.Kvs {
			or.Kvs = append(or.Kvs, toKeyValue(k))
		}
		out.Responses = append(out.Responses, or)
	}
	return out, nil
}

// toKeyValue converts a wire key-value to the server's.
func toKeyValue(k *transport.MVCCKeyValue) *kv.KeyValue {
	return &kv.KeyValue{
		Key:            k.Key,
		Value:          k.Value,
		CreateRevision: k.CreateRevision,
		ModRevision:    k.ModRevision,
		Version:        k.Version,
		LeaseID:        k.LeaseID,
	}
}

// toWireCompares converts the server's compares to the wire's; the target
// and result enums align by value.
func toWireCompares(cs []service.Compare) []transport.Compare {
	if len(cs) == 0 {
		return nil
	}
	out := make([]transport.Compare, 0, len(cs))
	for _, c := range cs {
		out = append(out, transport.Compare{
			Key:            c.Key,
			Target:         transport.CompareTarget(c.Target),
			Result:         transport.CompareResult(c.Result),
			Value:          c.Value,
			Version:        c.Version,
			CreateRevision: c.CreateRevision,
			ModRevision:    c.ModRevision,
		})
	}
	return out
}

// toWireOps converts the server's ops to the wire's; the op type enum aligns
// by value. An op of no known type has no wire form and is refused.
func toWireOps(ops []service.Op) ([]transport.Op, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	out := make([]transport.Op, 0, len(ops))
	for _, o := range ops {
		switch o.Type {
		case lock.OpPut, lock.OpGet, lock.OpDelete, lock.OpRange:
		default:
			return nil, fmt.Errorf("sqlitevfs: op type %d has no wire form", o.Type)
		}
		out = append(out, transport.Op{
			Type:     transport.OpType(o.Type),
			Key:      o.Key,
			Value:    o.Value,
			RangeEnd: o.RangeEnd,
			LeaseID:  o.LeaseID,
		})
	}
	return out, nil
}
