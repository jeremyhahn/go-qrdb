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

package errclass

// Condition is one sample of one table row, as the parity test sees it.
type Condition struct {
	// Row is the rule's name: the type or sentinel it recognises.
	Row string
	// Sample is an error the row must classify.
	Sample error
	// Want is the class the row assigns.
	Want Class
}

// Conditions lists every sample of every row, in table order.
//
// Want is read from the row itself rather than from Classify, so a row that
// an earlier row shadows shows up as a disagreement instead of agreeing with
// its own shadow.
func Conditions() []Condition {
	var out []Condition
	for _, r := range rules {
		for _, s := range r.samples {
			c, _ := r.classify(s)
			out = append(out, Condition{Row: r.name, Sample: s, Want: c})
		}
	}
	return out
}

// RuleCount reports how many rows the table has, for the discovery floor.
func RuleCount() int { return len(rules) }
