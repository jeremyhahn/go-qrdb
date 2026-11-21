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

func TestRegisterIdempotent(t *testing.T) {
	Reset()
	Register("timeSeries")
	Register("timeSeries")
	Register("timeSeries")

	caps := Get()
	if !caps.TimeSeries {
		t.Error("expected TimeSeries to be true after duplicate registrations")
	}
	if !Has("timeSeries") {
		t.Error("expected Has(timeSeries) to return true after duplicate registrations")
	}
}

func TestHasUnregisteredFeature(t *testing.T) {
	Reset()
	if Has("nonexistent") {
		t.Error("expected Has(nonexistent) to return false")
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

// TestRegisterAllOptionalFeatures registers every optional feature at once;
// TestRegisterOptionalFeature covers each one registered alone.
func TestRegisterAllOptionalFeatures(t *testing.T) {
	Reset()
	for _, tc := range optionalFeatureCases {
		Register(tc.feature)
	}

	caps := Get()
	for _, tc := range optionalFeatureCases {
		if !tc.field(caps) {
			t.Errorf("expected %s field to be true", tc.feature)
		}
		if !Has(tc.feature) {
			t.Errorf("expected Has(%s) to return true", tc.feature)
		}
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
