//go:build !qrdb_mysql

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

// registerMySQLFlags is a no-op without qrdb_mysql: there is no MySQL
// wire protocol server to configure, so --enable-mysql and --mysql-port
// do not exist on this binary. See start_mysql_flags.go for the
// qrdb_mysql counterpart.
func registerMySQLFlags(_ *cobra.Command, _ *startFlags) {}
