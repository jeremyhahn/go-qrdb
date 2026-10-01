// Copyright 2026 Jeremy Hahn
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

package sqltest

import (
	"context"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// CountingTableService counts the table definitions read through it and
// delegates every call, so a test can tell how many times a statement
// resolved a table.
type CountingTableService struct {
	service.TableServicer
	gets atomic.Int32
}

// GetTable implements service.TableServicer, counting the read.
func (c *CountingTableService) GetTable(ctx context.Context, database, name string) (*service.TableDef, error) {
	c.gets.Add(1)
	return c.TableServicer.GetTable(ctx, database, name)
}

// Gets returns the number of GetTable calls since the last Reset.
func (c *CountingTableService) Gets() int32 { return c.gets.Load() }

// Reset zeroes the count.
func (c *CountingTableService) Reset() { c.gets.Store(0) }

// CountTableReads makes h's registry serve its table catalog through a
// CountingTableService, which it returns.
func (h *Harness) CountTableReads() *CountingTableService {
	counting := &CountingTableService{TableServicer: h.TableSvc}
	h.Registry.(*FakeRegistry).TableServiceFn = func(string, string) (service.TableServicer, error) {
		return counting, nil
	}
	return counting
}
