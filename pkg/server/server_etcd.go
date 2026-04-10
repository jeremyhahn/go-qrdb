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

//go:build qrdb_etcd

package server

import (
	"github.com/jeremyhahn/go-qrdb/pkg/api/etcd"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

func init() {
	startEtcdServerFn = startEtcd
	stopEtcdServerFn = stopEtcd
}

// startEtcd creates and starts an etcd protocol server for the named database.
func startEtcd(s *Server, dbName string, cfg *database.EtcdConfig) (EtcdProtocolServer, error) {
	if err := etcd.ValidateConfig(cfg); err != nil {
		return nil, err
	}

	var opts []etcd.ServerOption

	if s.opts.tlsConfig != nil {
		opts = append(opts, etcd.WithTLS(s.opts.tlsConfig))
	}
	if s.opts.mvccSvc != nil {
		opts = append(opts, etcd.WithMVCC(s.opts.mvccSvc))
	}
	if s.opts.txSvc != nil {
		opts = append(opts, etcd.WithTransaction(s.opts.txSvc))
	}
	if s.opts.watchSvc != nil {
		opts = append(opts, etcd.WithWatch(s.opts.watchSvc))
	}
	if s.opts.leaseSvc != nil {
		opts = append(opts, etcd.WithLease(s.opts.leaseSvc))
	}
	if s.adminSvc != nil {
		opts = append(opts, etcd.WithAdmin(s.adminSvc))
	}

	srv := etcd.NewServer(dbName, cfg.ListenAddr, opts...)

	if err := srv.Start(); err != nil {
		return nil, err
	}

	s.etcdServers.Store(dbName, srv)
	s.logger.Info("etcd server started", "database", dbName, "addr", srv.Addr())

	return srv, nil
}

// stopEtcd stops a running etcd protocol server.
func stopEtcd(srv EtcdProtocolServer) {
	srv.Stop()
}
