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

// Condition is one sample of one classification rule: an error the rule must
// recognise, and the class it assigns. The rules carry their own samples, so
// Conditions is the whole table in a form a cross-package conformance check
// can iterate -- in particular the transport parity test, which has to live
// outside this package because every transport imports it.
type Condition struct {
	// Row is the rule's name: the type or sentinel it recognises.
	Row string
	// Sample is an error the row must classify.
	Sample error
	// Want is the class the row assigns.
	Want Class
}

// Conditions lists every sample of every rule, in table order.
//
// Want is read from the rule itself rather than from Classify, so a rule that
// an earlier rule shadows shows up as a disagreement instead of agreeing with
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

// RuleCount reports how many rules the table has, so a caller iterating
// Conditions can hold a discovery floor.
func RuleCount() int { return len(rules) }
