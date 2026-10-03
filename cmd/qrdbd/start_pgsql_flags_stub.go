//go:build !qrdb_pgsql

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

import "github.com/spf13/cobra"

// registerPgSQLFlags is a no-op without qrdb_pgsql: there is no
// PostgreSQL wire protocol server to configure, so --enable-pgsql and
// --pgsql-port do not exist on this binary. See start_pgsql_flags.go for
// the qrdb_pgsql counterpart.
func registerPgSQLFlags(_ *cobra.Command, _ *startFlags) {}
