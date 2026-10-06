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

// Package gorm connects GORM to qrdb's in-process database/sql driver
// through the stock gorm.io/driver/mysql dialector. It holds no dialector of
// its own: qrdb's SQL surface is go-mysql-server, which speaks MySQL, and
// sdk/go/driver returns go-sql-driver's *mysql.MySQLError for every engine
// error it classifies, so gorm.io/driver/mysql's clause rewriting, migrator
// and error translation apply unchanged.
//
// What this package adds is the qrdb half of the stock dialector's Config:
// the "qrdb" driver name, a DSN check that refuses the transports GORM cannot
// run over, and the string-size and datetime-precision defaults qrdb's
// engine is measured against.
//
//	import (
//	    "gorm.io/gorm"
//
//	    qrdbgorm "github.com/jeremyhahn/go-qrdb/sdk/go/gorm"
//	    _ "github.com/jeremyhahn/go-qrdb/sdk/go/driver/node" // registers node://
//	)
//
//	dialector, err := qrdbgorm.Open("node://memory?database=mydb")
//	if err != nil {
//	    return err
//	}
//	db, err := gorm.Open(dialector, &gorm.Config{TranslateError: true})
//
// The import is aliased because this package's name collides with
// gorm.io/gorm's. To reach a running qrdbd over its MySQL wire port, use
// gorm.io/driver/mysql's own Open with a go-sql-driver DSN; this package is
// only for the in-process schemes.
package gorm
