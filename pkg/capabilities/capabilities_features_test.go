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

import "testing"

// optionalFeatureCases maps every optional feature key to a getter for
// its corresponding Capabilities struct field.
var optionalFeatureCases = []struct {
	feature string
	field   func(Capabilities) bool
}{
	{"timeSeries", func(c Capabilities) bool { return c.TimeSeries }},
	{"vector", func(c Capabilities) bool { return c.Vector }},
	{"sql", func(c Capabilities) bool { return c.SQL }},
	{"mysql", func(c Capabilities) bool { return c.MySQL }},
	{"pgsql", func(c Capabilities) bool { return c.PostgreSQL }},
	{"etcd", func(c Capabilities) bool { return c.Etcd }},
	{"fts", func(c Capabilities) bool { return c.FTS }},
}

func TestRegisterOptionalFeature(t *testing.T) {
	for _, tc := range optionalFeatureCases {
		t.Run(tc.feature, func(t *testing.T) {
			Reset()
			Register(tc.feature)

			caps := Get()
			if !tc.field(caps) {
				t.Errorf("expected %s field to be true after registration", tc.feature)
			}
			if !Has(tc.feature) {
				t.Errorf("expected Has(%s) to return true", tc.feature)
			}

			// Every other optional feature must remain false.
			for _, other := range optionalFeatureCases {
				if other.feature == tc.feature {
					continue
				}
				if other.field(caps) {
					t.Errorf("expected %s field to remain false when only %s is registered", other.feature, tc.feature)
				}
			}
		})
	}
}

func TestUnregisteredOptionalFeature(t *testing.T) {
	for _, tc := range optionalFeatureCases {
		t.Run(tc.feature, func(t *testing.T) {
			Reset()

			caps := Get()
			if tc.field(caps) {
				t.Errorf("expected %s field to be false without registration", tc.feature)
			}
			if Has(tc.feature) {
				t.Errorf("expected Has(%s) to return false without registration", tc.feature)
			}
		})
	}
}

func TestResetClearsOptionalFeature(t *testing.T) {
	for _, tc := range optionalFeatureCases {
		t.Run(tc.feature, func(t *testing.T) {
			Register(tc.feature)
			Reset()

			caps := Get()
			if tc.field(caps) {
				t.Errorf("expected %s field to be false after Reset", tc.feature)
			}
			if Has(tc.feature) {
				t.Errorf("expected Has(%s) to be false after Reset", tc.feature)
			}
		})
	}
}
