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
	if s.opts.sealedWindowProbe != nil {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithSealedWindowProbe(s.opts.sealedWindowProbe))
	}
	// Every statement is bounded by sql.query_timeout: it is also what ends a
	// statement whose client has gone away (the listener does not watch the
	// socket while a statement runs).
	queryTimeout, err := s.config.SQL.QueryTimeoutDuration()
	if err != nil {
		return err
	}
	mysqlOpts = append(mysqlOpts, mysqlapi.WithQueryTimeout(queryTimeout), mysqlapi.WithClientLimiter(s.clientLimit))
	// sql.wait_timeout/sql.write_timeout bound idle time on the socket
	// itself: a connection sitting idle, including one holding an open
	// server-side cursor the client stopped fetching from, is closed and its
	// statement's resources released once it crosses waitTimeout -- the
	// upstream-gaps.md item 5 idle-cursor gap QueryTimeout does not reach,
	// because a cursor fetch is served without ever reaching the query
	// handler QueryTimeout bounds.
	waitTimeout, err := s.config.SQL.WaitTimeoutDuration()
	if err != nil {
		return err
	}
	writeTimeout, err := s.config.SQL.WriteTimeoutDuration()
	if err != nil {
		return err
	}
	mysqlOpts = append(mysqlOpts, mysqlapi.WithWaitTimeout(waitTimeout), mysqlapi.WithWriteTimeout(writeTimeout))
	// A cluster seal or cut-off closes every session it reaches: a session
	// outlives its statements, and some of what it holds is served without
	// another statement through the gate.
	if streams := s.clusterSealStreams(); streams != nil {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithSealStreams(streams))
	}
	if s.opts.dbManager != nil {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithDatabaseManager(s.opts.dbManager))
	}
	if s.opts.databaseSvc != nil {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithDatabaseService(s.opts.databaseSvc))
	}
	if s.config.SQL.DefaultDatabase != "" {
		mysqlOpts = append(mysqlOpts,
			mysqlapi.WithDatabaseName(s.config.SQL.DefaultDatabase),
			mysqlapi.WithSharedDefaultDatabase(s.config.SQL.DefaultDatabase))
	}
	if s.opts.collectionSvc != nil {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.indexSvc != nil {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithIndex(s.opts.indexSvc))
	}
	// The transport-neutral per-(tenant, database) service.Registry closes
	// the isolation gap MySQL wire-protocol sessions would otherwise have:
	// without it, relational tables are invisible and every registry-backed
	// resolution (barrier/quota/isolation enforcement, IsolationRaft/
	// IsolationVolume routing) is bypassed entirely. See buildServiceRegistry.
	if reg := s.buildServiceRegistry(); reg != nil {
		mysqlOpts = append(mysqlOpts, mysqlapi.WithServiceRegistry(reg))
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
