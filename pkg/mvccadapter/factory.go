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

package mvccadapter

import (
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// NewMVCCFastCreateFunc returns an sm.CreateFunc that produces a
// FastStateMachine with MVCC support for each shard/replica pair.
// The storeFactory provides the backing storage; a RaftStorage and
// Adapter are created automatically for each instance.
func NewMVCCFastCreateFunc(
	storeFactory func(shardID, replicaID uint64) storage.Storage,
	log *logger.Logger,
	opts ...statemachine.Option,
) sm.CreateFunc {
	return sm.NewCreateFunc(func(shardID, replicaID uint64) sm.StateMachine {
		s := storeFactory(shardID, replicaID)
		rs, err := mvcc.NewRaftStorage(&mvcc.RaftStorageConfig{
			Store:  s,
			Logger: log,
		})
		if err != nil {
			panic("mvccadapter: failed to create MVCC storage: " + err.Error())
		}
		adapter := NewAdapter(rs)
		allOpts := make([]statemachine.Option, 0, len(opts)+1)
		allOpts = append(allOpts, statemachine.WithMVCCStore(adapter))
		allOpts = append(allOpts, opts...)
		return statemachine.NewFastStateMachine(shardID, replicaID, s, allOpts...)
	})
}
