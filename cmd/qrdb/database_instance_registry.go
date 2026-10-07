//go:build qrdb_embedded

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
	"sync"
	"sync/atomic"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	vectorstore "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// embeddedDatabaseInstance holds the resources for a single embedded database.
type embeddedDatabaseInstance struct {
	host          *quicraft.Host
	localBackend  *backend.LocalBackend
	watchHub      *watch.Hub
	vectorStorage *vectorstore.PebbleVectorStorage
}

// Close releases all resources held by the instance.
func (i *embeddedDatabaseInstance) Close() error {
	var firstErr error
	if i.watchHub != nil {
		i.watchHub.Close()
	}
	if i.vectorStorage != nil {
		if err := i.vectorStorage.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if i.host != nil {
		if err := i.host.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if i.localBackend != nil {
		if err := i.localBackend.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DatabaseInstanceRegistry manages per-database embedded instances using
// a lock-free sync.Map for concurrent access.
type DatabaseInstanceRegistry struct {
	logger    *sdk.ConcreteLogger
	instances sync.Map // name (string) -> *embeddedDatabaseInstance
	count     atomic.Int64
}

// NewDatabaseInstanceRegistry creates a new empty registry.
func NewDatabaseInstanceRegistry(log *sdk.ConcreteLogger) *DatabaseInstanceRegistry {
	return &DatabaseInstanceRegistry{logger: log}
}

// Register adds an instance to the registry. Returns InstanceAlreadyExistsError
// if the name is already registered.
func (r *DatabaseInstanceRegistry) Register(name string, inst *embeddedDatabaseInstance) error {
	if _, loaded := r.instances.LoadOrStore(name, inst); loaded {
		return &InstanceAlreadyExistsError{Name: name}
	}
	r.count.Add(1)
	r.logger.Debug("database instance registered", "name", name)
	return nil
}

// Get returns the instance for the given database name.
func (r *DatabaseInstanceRegistry) Get(name string) (*embeddedDatabaseInstance, error) {
	val, ok := r.instances.Load(name)
	if !ok {
		return nil, &InstanceNotFoundError{Name: name}
	}
	return val.(*embeddedDatabaseInstance), nil //nolint:errcheck // type assertion on a value this container only ever holds
}

// Stop stops and removes a single database instance.
func (r *DatabaseInstanceRegistry) Stop(name string) error {
	val, loaded := r.instances.LoadAndDelete(name)
	if !loaded {
		return &InstanceNotFoundError{Name: name}
	}
	r.count.Add(-1)
	inst := val.(*embeddedDatabaseInstance) //nolint:errcheck // type assertion on a value this container only ever holds
	r.logger.Debug("stopping database instance", "name", name)
	return inst.Close()
}

// StopAll stops and removes all registered instances.
func (r *DatabaseInstanceRegistry) StopAll() error {
	var firstErr error
	r.instances.Range(func(key, value any) bool {
		name := key.(string)                      //nolint:errcheck // type assertion on a value this container only ever holds
		inst := value.(*embeddedDatabaseInstance) //nolint:errcheck // type assertion on a value this container only ever holds
		r.logger.Debug("stopping database instance", "name", name)
		if err := inst.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		r.instances.Delete(key)
		r.count.Add(-1)
		return true
	})
	return firstErr
}

// Count returns the number of registered instances.
func (r *DatabaseInstanceRegistry) Count() int64 {
	return r.count.Load()
}

// Names returns all registered instance names.
func (r *DatabaseInstanceRegistry) Names() []string {
	var names []string
	r.instances.Range(func(key, _ any) bool {
		names = append(names, key.(string)) //nolint:errcheck // type assertion on a value this container only ever holds
		return true
	})
	return names
}
