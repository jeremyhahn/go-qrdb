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

package capabilities

import (
	"sync"
	"testing"
)

func TestGetDefaultCapabilities(t *testing.T) {
	Reset()
	caps := Get()

	if !caps.KV {
		t.Error("expected KV to be true")
	}
	if !caps.MVCC {
		t.Error("expected MVCC to be true")
	}
	if !caps.Transaction {
		t.Error("expected Transaction to be true")
	}
	if !caps.Batch {
		t.Error("expected Batch to be true")
	}
	if !caps.Watch {
		t.Error("expected Watch to be true")
	}
	if !caps.Lease {
		t.Error("expected Lease to be true")
	}
	if !caps.Index {
		t.Error("expected Index to be true")
	}
	if caps.TimeSeries {
		t.Error("expected TimeSeries to be false without build tag")
	}
	if caps.Vector {
		t.Error("expected Vector to be false without build tag")
	}
	if caps.MySQL {
		t.Error("expected MySQL to be false without build tag")
	}
	if caps.PostgreSQL {
		t.Error("expected PostgreSQL to be false without build tag")
	}
}

func TestGetDefaultCapabilitiesCoreAlwaysTrue(t *testing.T) {
	Reset()
	caps := Get()

	coreFields := map[string]bool{
		"KV":          caps.KV,
		"MVCC":        caps.MVCC,
		"Transaction": caps.Transaction,
		"Batch":       caps.Batch,
		"Watch":       caps.Watch,
		"Lease":       caps.Lease,
		"Index":       caps.Index,
	}
	for name, val := range coreFields {
		if !val {
			t.Errorf("core capability %s should always be true", name)
		}
	}
}

func TestRegisterTimeSeries(t *testing.T) {
	Reset()
	Register("timeSeries")

	caps := Get()
	if !caps.TimeSeries {
		t.Error("expected TimeSeries to be true after registration")
	}
	if caps.Vector {
		t.Error("expected Vector to remain false")
	}
}

func TestRegisterTimeSeriesHas(t *testing.T) {
	Reset()
	Register("timeSeries")

	if !Has("timeSeries") {
		t.Error("expected Has(timeSeries) to return true")
	}
	if Has("vector") {
		t.Error("expected Has(vector) to return false")
	}
}

func TestRegisterVector(t *testing.T) {
	Reset()
	Register("vector")

	caps := Get()
	if !caps.Vector {
		t.Error("expected Vector to be true after registration")
	}
	if caps.TimeSeries {
		t.Error("expected TimeSeries to remain false")
	}
}

func TestRegisterVectorHas(t *testing.T) {
	Reset()
	Register("vector")

	if !Has("vector") {
		t.Error("expected Has(vector) to return true")
	}
	if Has("timeSeries") {
		t.Error("expected Has(timeSeries) to return false")
	}
}

func TestRegisterBothFeatures(t *testing.T) {
	Reset()
	Register("timeSeries")
	Register("vector")

	caps := Get()
	if !caps.TimeSeries {
		t.Error("expected TimeSeries to be true")
	}
	if !caps.Vector {
		t.Error("expected Vector to be true")
	}
}

func TestRegisterBothFeaturesHas(t *testing.T) {
	Reset()
	Register("timeSeries")
	Register("vector")

	if !Has("timeSeries") {
		t.Error("expected Has(timeSeries) to return true")
	}
	if !Has("vector") {
		t.Error("expected Has(vector) to return true")
	}
}

func TestRegisterIdempotent(t *testing.T) {
	Reset()
	Register("timeSeries")
	Register("timeSeries")
	Register("timeSeries")

	caps := Get()
	if !caps.TimeSeries {
		t.Error("expected TimeSeries to be true after duplicate registrations")
	}
}

func TestRegisterIdempotentHas(t *testing.T) {
	Reset()
	Register("vector")
	Register("vector")

	if !Has("vector") {
		t.Error("expected Has(vector) to return true after duplicate registrations")
	}
}

func TestResetClearsFeatures(t *testing.T) {
	Register("timeSeries")
	Register("vector")
	Reset()

	caps := Get()
	if caps.TimeSeries {
		t.Error("expected TimeSeries to be false after Reset")
	}
	if caps.Vector {
		t.Error("expected Vector to be false after Reset")
	}
}

func TestResetClearsFeaturesHas(t *testing.T) {
	Register("timeSeries")
	Register("vector")
	Reset()

	if Has("timeSeries") {
		t.Error("expected Has(timeSeries) to be false after Reset")
	}
	if Has("vector") {
		t.Error("expected Has(vector) to be false after Reset")
	}
}

func TestHasUnregisteredFeature(t *testing.T) {
	Reset()
	if Has("nonexistent") {
		t.Error("expected Has(nonexistent) to return false")
	}
}

func TestHasUnregisteredFeatureEmpty(t *testing.T) {
	Reset()
	if Has("") {
		t.Error("expected Has(\"\") to return false")
	}
}

func TestConcurrentRegistration(t *testing.T) {
	Reset()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			Register("timeSeries")
		}()
		go func() {
			defer wg.Done()
			Register("vector")
		}()
	}
	wg.Wait()

	caps := Get()
	if !caps.TimeSeries {
		t.Error("expected TimeSeries to be true after concurrent registration")
	}
	if !caps.Vector {
		t.Error("expected Vector to be true after concurrent registration")
	}
}

func TestRegisterMySQL(t *testing.T) {
	Reset()
	Register("mysql")

	caps := Get()
	if !caps.MySQL {
		t.Error("expected MySQL to be true after registration")
	}
	if caps.PostgreSQL {
		t.Error("expected PostgreSQL to remain false")
	}
}

func TestRegisterMySQLHas(t *testing.T) {
	Reset()
	Register("mysql")

	if !Has("mysql") {
		t.Error("expected Has(mysql) to return true")
	}
	if Has("pgsql") {
		t.Error("expected Has(pgsql) to return false")
	}
}

func TestRegisterPgSQL(t *testing.T) {
	Reset()
	Register("pgsql")

	caps := Get()
	if !caps.PostgreSQL {
		t.Error("expected PostgreSQL to be true after registration")
	}
	if caps.MySQL {
		t.Error("expected MySQL to remain false")
	}
}

func TestRegisterPgSQLHas(t *testing.T) {
	Reset()
	Register("pgsql")

	if !Has("pgsql") {
		t.Error("expected Has(pgsql) to return true")
	}
	if Has("mysql") {
		t.Error("expected Has(mysql) to return false")
	}
}

func TestRegisterBothSQLProtocols(t *testing.T) {
	Reset()
	Register("mysql")
	Register("pgsql")

	caps := Get()
	if !caps.MySQL {
		t.Error("expected MySQL to be true")
	}
	if !caps.PostgreSQL {
		t.Error("expected PostgreSQL to be true")
	}
}

func TestRegisterBothSQLProtocolsHas(t *testing.T) {
	Reset()
	Register("mysql")
	Register("pgsql")

	if !Has("mysql") {
		t.Error("expected Has(mysql) to return true")
	}
	if !Has("pgsql") {
		t.Error("expected Has(pgsql) to return true")
	}
}

func TestRegisterAllOptionalFeatures(t *testing.T) {
	Reset()
	Register("timeSeries")
	Register("vector")
	Register("mysql")
	Register("pgsql")

	caps := Get()
	if !caps.TimeSeries {
		t.Error("expected TimeSeries to be true")
	}
	if !caps.Vector {
		t.Error("expected Vector to be true")
	}
	if !caps.MySQL {
		t.Error("expected MySQL to be true")
	}
	if !caps.PostgreSQL {
		t.Error("expected PostgreSQL to be true")
	}
}

func TestResetClearsSQLFeatures(t *testing.T) {
	Register("mysql")
	Register("pgsql")
	Reset()

	caps := Get()
	if caps.MySQL {
		t.Error("expected MySQL to be false after Reset")
	}
	if caps.PostgreSQL {
		t.Error("expected PostgreSQL to be false after Reset")
	}
}

func TestResetClearsSQLFeaturesHas(t *testing.T) {
	Register("mysql")
	Register("pgsql")
	Reset()

	if Has("mysql") {
		t.Error("expected Has(mysql) to be false after Reset")
	}
	if Has("pgsql") {
		t.Error("expected Has(pgsql) to be false after Reset")
	}
}

func TestConcurrentGetAndRegister(t *testing.T) {
	Reset()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			Register("timeSeries")
		}()
		go func() {
			defer wg.Done()
			_ = Get()
		}()
		go func() {
			defer wg.Done()
			_ = Has("timeSeries")
		}()
	}
	wg.Wait()

	caps := Get()
	if !caps.TimeSeries {
		t.Error("expected TimeSeries to be true after concurrent operations")
	}
}
