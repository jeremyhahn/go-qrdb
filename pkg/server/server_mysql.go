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

//go:build qrdb_mysql

package server

import (
	"net"
	"strconv"

	mysqlapi "github.com/jeremyhahn/go-qrdb/pkg/sql/mysql"
)

func init() {
	startMySQLFn = startMySQL
	stopMySQLFn = stopMySQL
}

// startMySQL creates and starts the MySQL wire protocol server.
func startMySQL(s *Server) error {
	addr := net.JoinHostPort(s.config.Server.Host, strconv.Itoa(s.config.Server.MySQLPort))

	var mysqlOpts []mysqlapi.ServerOption

	if s.opts.tlsConfig != nil {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithTLS(s.opts.tlsConfig))
	}
	if s.opts.authMW != nil {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithAuthMiddleware(s.opts.authMW))
	}
	if s.opts.dbManager != nil {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithDatabaseManager(s.opts.dbManager))
	}
	if s.config.SQL.DefaultDatabase != "" {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithDatabaseName(s.config.SQL.DefaultDatabase))
	}

	s.mysqlSrv = mysqlapi.NewServer(s.kvSvc, s.adminSvc, addr, mysqlOpts...)

	if err := s.mysqlSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("MySQL server started", "addr", s.mysqlSrv.Addr())
	return nil
}

// stopMySQL stops the MySQL server if it was started.
func stopMySQL(s *Server) {
	if s.mysqlSrv != nil {
		s.mysqlSrv.Stop()
	}
}
