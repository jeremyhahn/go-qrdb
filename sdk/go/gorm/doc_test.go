//go:build qrdb_sql

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

// Example builds a dialector for an in-process qrdb database. It does not
// open a connection, so it runs offline: the dialector is gorm.io/driver/mysql's,
// configured to open the DSN through qrdb's "qrdb" database/sql driver.
func Example() {
	dialector, err := qrdbgorm.Open("embedded://memory?database=example")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(dialector.Name(), dialector.DriverName)
	// Output: mysql qrdb
}

// Example_networkTransport shows the refusal a network SQL DSN gets.
func Example_networkTransport() {
	_, err := qrdbgorm.Open("rest://db.example.com:8080/app")
	fmt.Println(err != nil)
	// Output: true
}
