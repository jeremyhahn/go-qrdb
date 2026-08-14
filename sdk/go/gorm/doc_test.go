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

package gorm_test

import (
	"fmt"

	qrdbgorm "github.com/jeremyhahn/go-qrdb/sdk/go/gorm"
)

// Example demonstrates building a Dialector for an embedded qrdb database,
// as shown in the package doc comment. It does not open a connection, so it
// runs offline: Name reports the dialect GORM treats the connection as
// speaking (see the package doc comment for why that is "mysql", not
// "qrdb"), independent of whether Initialize ever succeeds.
func Example() {
	dialector := qrdbgorm.Open("embedded://memory?database=example")
	fmt.Println(dialector.Name())
	// Output: mysql
}

// Example_dialectName demonstrates overriding the reported dialect name via
// Config.DialectName, for a caller that needs to disambiguate multiple
// dialectors (in logs, for instance) rather than rely on the "mysql"
// default.
func Example_dialectName() {
	dialector := qrdbgorm.New(qrdbgorm.Config{
		DSN:         "embedded://memory?database=example",
		DialectName: "qrdb",
	})
	fmt.Println(dialector.Name())
	// Output: qrdb
}
