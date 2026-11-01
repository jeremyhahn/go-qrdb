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

//go:build qrdb_pgsql

package server

import (
	"net"
	"strconv"

	pgsqlapi "github.com/jeremyhahn/go-qrdb/pkg/sql/pgsql"
)

func init() {
	startPgSQLFn = startPgSQL
	stopPgSQLFn = stopPgSQL
}

// startPgSQL creates and starts the PostgreSQL wire protocol server.
func startPgSQL(s *Server) error {
	addr := net.JoinHostPort(s.config.Server.Host, strconv.Itoa(s.config.Server.PgSQLPort))

	var pgsqlOpts []pgsqlapi.ServerOption

	if s.opts.tlsConfig != nil {
		pgsqlOpts = append(pgsqlOpts, pgsqlapi.WithTLS(s.opts.tlsConfig))
	}
	if s.opts.authMW != nil {
		pgsqlOpts = append(pgsqlOpts, pgsqlapi.WithAuthMiddleware(s.opts.authMW))
	}
	if s.opts.sealedWindowProbe != nil {
		pgsqlOpts = append(pgsqlOpts, pgsqlapi.WithSealedWindowProbe(s.opts.sealedWindowProbe))
	}
	// Every statement is bounded by sql.query_timeout, as on the MySQL wire.
	queryTimeout, err := s.config.SQL.QueryTimeoutDuration()
	if err != nil {
		return err
	}
	pgsqlOpts = append(pgsqlOpts, pgsqlapi.WithQueryTimeout(queryTimeout), pgsqlapi.WithClientLimiter(s.clientLimit))
	// A cluster seal or cut-off closes every session it reaches: a session
	// outlives its statements, and some of what it holds is served without
	// another statement through the gate.
	if streams := s.clusterSealStreams(); streams != nil {
		pgsqlOpts = append(pgsqlOpts, pgsqlapi.WithSealStreams(streams))
	}
	if s.opts.dbManager != nil {
		pgsqlOpts = append(pgsqlOpts, pgsqlapi.WithDatabaseManager(s.opts.dbManager))
	}
	if s.opts.databaseSvc != nil {
		pgsqlOpts = append(pgsqlOpts, pgsqlapi.WithDatabaseService(s.opts.databaseSvc))
	}
	if s.config.SQL.DefaultDatabase != "" {
		pgsqlOpts = append(pgsqlOpts,
			pgsqlapi.WithDatabaseName(s.config.SQL.DefaultDatabase),
			pgsqlapi.WithSharedDefaultDatabase(s.config.SQL.DefaultDatabase))
	}
	if s.opts.collectionSvc != nil {
		pgsqlOpts = append(pgsqlOpts, pgsqlapi.WithCollectionService(s.opts.collectionSvc))
	}
	if s.opts.indexSvc != nil {
		pgsqlOpts = append(pgsqlOpts, pgsqlapi.WithIndex(s.opts.indexSvc))
	}
	// The transport-neutral per-(tenant, database) service.Registry closes
	// the isolation gap PostgreSQL wire-protocol sessions would otherwise
	// have: without it, relational tables are invisible and every
	// registry-backed resolution (barrier/quota/isolation enforcement,
	// IsolationRaft/IsolationVolume routing) is bypassed entirely. See
	// buildServiceRegistry.
	if reg := s.buildServiceRegistry(); reg != nil {
		pgsqlOpts = append(pgsqlOpts, pgsqlapi.WithServiceRegistry(reg))
	}

	s.pgsqlSrv = pgsqlapi.NewServer(s.kvSvc, s.adminSvc, addr, pgsqlOpts...)

	if err := s.pgsqlSrv.Start(); err != nil {
		return err
	}

	s.logger.Info("PostgreSQL server started", "addr", s.pgsqlSrv.Addr())
	return nil
}

// stopPgSQL stops the PostgreSQL server if it was started.
func stopPgSQL(s *Server) {
	if s.pgsqlSrv != nil {
		s.pgsqlSrv.Stop()
	}
}
