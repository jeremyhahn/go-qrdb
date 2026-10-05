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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// NodeCompaction returns the platform's node-keyspace compaction policy
// through the embedded admin service. The service refuses a context bound to
// any tenant but the system tenant.
func (t *Transport) NodeCompaction(ctx context.Context) (*transport.DatabaseCompactionConfig, error) {
	if err := t.gate("NodeCompaction"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "NodeCompaction"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	policy, err := t.adminSvc.NodeCompaction(ctx)
	if err != nil {
		return nil, translateError(err)
	}
	return dbwire.Compaction(policy), nil
}

// SetNodeCompaction sets the platform's node-keyspace compaction policy
// through the embedded admin service; nil turns it off. The service refuses
// a context bound to any tenant but the system tenant.
func (t *Transport) SetNodeCompaction(ctx context.Context, policy *transport.DatabaseCompactionConfig) (*transport.DatabaseCompactionConfig, error) {
	if err := t.gate("SetNodeCompaction"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "SetNodeCompaction"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	p := fromTransportCompactionConfig(policy)
	if err := t.adminSvc.SetNodeCompaction(ctx, p); err != nil {
		return nil, translateError(err)
	}
	return dbwire.Compaction(p.Normalized()), nil
}
