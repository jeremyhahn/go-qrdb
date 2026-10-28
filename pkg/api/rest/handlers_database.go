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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Database request/response types
// ---------------------------------------------------------------------------

// createDatabaseRequest is the JSON payload for POST /v1/databases.
type createDatabaseRequest struct {
	Name         string `json:"name"`
	Backend      string `json:"backend"`
	DataDir      string `json:"dataDir,omitempty"`
	Replicas     int    `json:"replicas,omitempty"`
	DedicatedWAL bool   `json:"dedicatedWal,omitempty"`
	Protocol     struct {
		MySQL      bool            `json:"mysql"`
		PostgreSQL bool            `json:"postgresql"`
		Etcd       *etcdConfigJSON `json:"etcd,omitempty"`
	} `json:"protocol"`
	Raft    *raftConfigRequest    `json:"raft,omitempty"`
	Barrier *barrierConfigRequest `json:"barrier,omitempty"`
	SQL     *sqlConfigJSON        `json:"sql,omitempty"`
}

// sqlConfigJSON is the JSON representation of a database's SQL settings, in
// requests and responses alike. See docs/row-locking/api.md.
type sqlConfigJSON struct {
	Concurrency string `json:"concurrency,omitempty"`
}

// updateDatabaseSQLRequest is the JSON payload for PATCH /v1/databases/{name}/sql.
type updateDatabaseSQLRequest struct {
	SQL *sqlConfigJSON `json:"sql"`
}

// compactionConfigJSON is the JSON representation of a database's
// scheduled-compaction policy, requests and responses alike. See
// docs/databases/api.md.
type compactionConfigJSON struct {
	Mode            string `json:"mode,omitempty"`
	RetainRevisions int64  `json:"retainRevisions,omitempty"`
	RetainPeriod    string `json:"retainPeriod,omitempty"`
}

// updateDatabaseCompactionRequest is the JSON payload for
// PATCH /v1/databases/{name}/compaction. An absent compaction turns the
// policy off.
type updateDatabaseCompactionRequest struct {
	Compaction *compactionConfigJSON `json:"compaction"`
}

// compactionConfigFromJSON converts a request's compaction policy; nil stays
// nil (off).
func compactionConfigFromJSON(c *compactionConfigJSON) *compaction.Policy {
	if c == nil {
		return nil
	}
	return &compaction.Policy{
		Mode:            compaction.Mode(c.Mode),
		RetainRevisions: c.RetainRevisions,
		RetainPeriod:    c.RetainPeriod,
	}
}

// sqlConfigFromJSON converts a request's SQL settings; nil stays nil (the
// defaults).
func sqlConfigFromJSON(c *sqlConfigJSON) *database.SQLConfig {
	if c == nil {
		return nil
	}
	return &database.SQLConfig{Concurrency: database.Concurrency(c.Concurrency)}
}

// barrierConfigRequest is the JSON representation of encryption barrier
// configuration in a database creation request. Secret carries the barrier
// passphrase for the initial Initialize call; it is never persisted (the
// DatabaseBarrierConfig in the database's catalog record has no secret
// field) and is zeroed as soon as it has been copied into the
// seal.Credentials passed to EnsureBarrier.
type barrierConfigRequest struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode,omitempty"`
	SharesRequired int    `json:"sharesRequired,omitempty"`
	SharesTotal    int    `json:"sharesTotal,omitempty"`
	Secret         string `json:"secret,omitempty"`
}

// raftConfigRequest is the JSON representation of Raft consensus configuration
// in a database creation request.
type raftConfigRequest struct {
	ClusterMode    string                           `json:"clusterMode"`
	ReplicaID      uint64                           `json:"replicaId,omitempty"`
	RaftAddress    string                           `json:"raftAddress,omitempty"`
	Peers          []string                         `json:"peers,omitempty"`
	LocalNodeCount int                              `json:"localNodeCount,omitempty"`
	Discovery      string                           `json:"discovery,omitempty"`
	Static         *staticDiscoveryConfigRequest    `json:"static,omitempty"`
	Multicast      *multicastDiscoveryConfigRequest `json:"multicast,omitempty"`
	DNS            *dnsDiscoveryConfigRequest       `json:"dns,omitempty"`
	Token          *tokenDiscoveryConfigRequest     `json:"token,omitempty"`
}

// staticDiscoveryConfigRequest is the JSON representation of static peer
// discovery configuration in a request payload.
type staticDiscoveryConfigRequest struct {
	Peers        []string `json:"peers,omitempty"`
	SharedSecret string   `json:"sharedSecret,omitempty"`
}

// multicastDiscoveryConfigRequest is the JSON representation of UDP multicast
// peer discovery configuration in a request payload.
type multicastDiscoveryConfigRequest struct {
	SharedSecret     string `json:"sharedSecret"`
	MulticastGroup   string `json:"multicastGroup,omitempty"`
	Interface        string `json:"interface,omitempty"`
	MinPeers         int    `json:"minPeers,omitempty"`
	AnnounceInterval string `json:"announceInterval,omitempty"`
	DiscoverTimeout  string `json:"discoverTimeout,omitempty"`
}

// dnsDiscoveryConfigRequest is the JSON representation of DNS SRV record
// peer discovery configuration in a request payload.
type dnsDiscoveryConfigRequest struct {
	Service       string `json:"service,omitempty"`
	Proto         string `json:"proto,omitempty"`
	Domain        string `json:"domain"`
	LookupTimeout string `json:"lookupTimeout,omitempty"`
}

// tokenDiscoveryConfigRequest is the JSON representation of token-based
// discovery configuration in a request payload.
type tokenDiscoveryConfigRequest struct {
	Endpoint    string `json:"endpoint"`
	Token       string `json:"token"`
	ClusterSize int    `json:"clusterSize"`
	Timeout     string `json:"timeout,omitempty"`
}

// updateDatabaseProtocolRequest is the JSON payload for PATCH /v1/databases/{name}/protocol.
type updateDatabaseProtocolRequest struct {
	MySQL      bool `json:"mysql"`
	PostgreSQL bool `json:"postgresql"`
}

// etcdConfigJSON is the JSON representation of a database's etcd wire
// protocol configuration, in requests and responses alike: both
// "database create"'s protocol.etcd block and the dedicated
// PATCH /v1/databases/{name}/etcd body carry the same three fields.
type etcdConfigJSON struct {
	Enabled             bool     `json:"enabled"`
	ListenAddr          string   `json:"listenAddr"`
	AdvertiseClientURLs []string `json:"advertiseClientUrls,omitempty"`
}

// updateDatabaseEtcdRequest is the JSON payload for PATCH /v1/databases/{name}/etcd.
type updateDatabaseEtcdRequest = etcdConfigJSON

// updateDatabaseRaftRequest is the JSON payload for PATCH /v1/databases/{name}/raft.
type updateDatabaseRaftRequest struct {
	ClusterMode    string                           `json:"clusterMode"`
	ReplicaID      uint64                           `json:"replicaId,omitempty"`
	RaftAddress    string                           `json:"raftAddress,omitempty"`
	Peers          []string                         `json:"peers,omitempty"`
	LocalNodeCount int                              `json:"localNodeCount,omitempty"`
	Discovery      string                           `json:"discovery,omitempty"`
	Static         *staticDiscoveryConfigRequest    `json:"static,omitempty"`
	Multicast      *multicastDiscoveryConfigRequest `json:"multicast,omitempty"`
	DNS            *dnsDiscoveryConfigRequest       `json:"dns,omitempty"`
	Token          *tokenDiscoveryConfigRequest     `json:"token,omitempty"`
}

// scaleDatabaseRequest is the JSON payload for PATCH /v1/databases/{name}/scale.
type scaleDatabaseRequest struct {
	TargetReplicas int `json:"target_replicas"`
}

// updateDatabaseBarrierRequest is the JSON payload for PATCH /v1/databases/{name}/barrier.
type updateDatabaseBarrierRequest struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode,omitempty"`
	SharesRequired int    `json:"sharesRequired,omitempty"`
	SharesTotal    int    `json:"sharesTotal,omitempty"`
}

// updateDatabaseConnectionRequest is the JSON payload for PATCH /v1/databases/{name}/connection.
type updateDatabaseConnectionRequest struct {
	Transport string `json:"transport"`
	Address   string `json:"address"`
	CAFile    string `json:"caFile,omitempty"`
	CertFile  string `json:"certFile,omitempty"`
	KeyFile   string `json:"keyFile,omitempty"`
}

// listDatabasesResponse is the JSON payload returned by GET /v1/databases/list.
type listDatabasesResponse struct {
	Databases []transport.DatabaseInfo `json:"databases"`
}

// ---------------------------------------------------------------------------
// Database handlers
// ---------------------------------------------------------------------------

// handleCreateDatabase handles POST /v1/databases -- creates a new database
// with the specified engine and backend configuration.
func (s *Server) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	if s.databaseSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "database"})
		return
	}

	// Strict decoding: an unknown member (a stale "engine" key, OD-13) is
	// refused rather than silently ignored, the same generic rule
	// decodeGuardedRequest applies to every state-changing endpoint.
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req createDatabaseRequest
	if err := dec.Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	cfg := database.DatabaseConfig{
		Name:         req.Name,
		Backend:      database.BackendType(req.Backend),
		DataDir:      req.DataDir,
		Replicas:     req.Replicas,
		DedicatedWAL: req.DedicatedWAL,
		Protocol: database.ProtocolConfig{
			MySQL:      req.Protocol.MySQL,
			PostgreSQL: req.Protocol.PostgreSQL,
		},
	}
	if req.Protocol.Etcd != nil {
		cfg.Protocol.Etcd = &database.EtcdConfig{
			Enabled:             req.Protocol.Etcd.Enabled,
			ListenAddr:          req.Protocol.Etcd.ListenAddr,
			AdvertiseClientURLs: req.Protocol.Etcd.AdvertiseClientURLs,
		}
	}
	if req.Raft != nil {
		cfg.Raft = raftConfigFromRequest(req.Raft)
	}
	cfg.SQL = sqlConfigFromJSON(req.SQL)

	var creds seal.Credentials
	if req.Barrier != nil {
		cfg.Barrier = &database.DatabaseBarrierConfig{
			Enabled:        req.Barrier.Enabled,
			Mode:           req.Barrier.Mode,
			SharesRequired: req.Barrier.SharesRequired,
			SharesTotal:    req.Barrier.SharesTotal,
		}
		var err error
		if creds, err = seal.CredentialsFor(r.Context(), []byte(req.Barrier.Secret)); err != nil {
			writeErrorJSON(w, mapHTTPError(err), err)
			return
		}
		defer seal.DestroyCredentials(creds)
		req.Barrier.Secret = ""
	}

	info, err := s.createDatabaseForTenant(r.Context(), cfg, creds)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(dbwire.Info(info))
}

// createDatabaseForTenant creates cfg via the tenant-scoped path, threading
// creds through when the resolved DatabaseServicer supports credentialed
// creation. See service.CreateDatabaseForTenantWithCredentials, the shared
// helper every transport (REST, gRPC, QUIC, MCP, Unix) delegates to.
func (s *Server) createDatabaseForTenant(ctx context.Context, cfg database.DatabaseConfig, creds seal.Credentials) (*database.DatabaseInfo, error) {
	return service.CreateDatabaseForTenantWithCredentials(ctx, s.databaseSvc, cfg, creds)
}

// handleListDatabases handles GET /v1/databases/list -- returns information
// about all open databases.
func (s *Server) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	if s.databaseSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "database"})
		return
	}

	dbs, err := s.databaseSvc.ListForTenant(r.Context())
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, listDatabasesResponse{Databases: dbwire.Infos(dbs)})
}

// handleDatabaseByName routes GET /v1/databases/{name},
// DELETE /v1/databases/{name}, PATCH /v1/databases/{name}/protocol,
// PATCH /v1/databases/{name}/etcd, PATCH /v1/databases/{name}/raft,
// PATCH /v1/databases/{name}/barrier, PATCH /v1/databases/{name}/connection,
// PATCH /v1/databases/{name}/sql, and PATCH /v1/databases/{name}/scale to the
// appropriate handler
// using the database name extracted from the URL path.
func (s *Server) handleDatabaseByName(w http.ResponseWriter, r *http.Request) {
	if s.databaseSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "database"})
		return
	}

	// Extract database name (and optional sub-path) from path:
	// /v1/databases/{name} or /v1/databases/{name}/protocol
	remainder := strings.TrimPrefix(r.URL.Path, "/v1/databases/")
	if remainder == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}

	// Check for sub-resource paths like {name}/protocol.
	name, subPath, _ := strings.Cut(remainder, "/")
	if name == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}

	// Route sub-paths.
	if subPath == "protocol" && r.Method == http.MethodPatch {
		s.handleUpdateDatabaseProtocol(w, r, name)
		return
	}
	if subPath == "etcd" && r.Method == http.MethodPatch {
		s.handleUpdateDatabaseEtcd(w, r, name)
		return
	}
	if subPath == "raft" && r.Method == http.MethodPatch {
		s.handleUpdateDatabaseRaft(w, r, name)
		return
	}
	if subPath == "barrier" && r.Method == http.MethodPatch {
		s.handleUpdateDatabaseBarrier(w, r, name)
		return
	}
	if subPath == "scale" && r.Method == http.MethodPatch {
		s.handleScaleDatabase(w, r, name)
		return
	}
	if subPath == "connection" && r.Method == http.MethodPatch {
		s.handleUpdateDatabaseConnection(w, r, name)
		return
	}
	if subPath == "sql" && r.Method == http.MethodPatch {
		s.handleUpdateDatabaseSQL(w, r, name)
		return
	}
	if subPath == "compaction" && r.Method == http.MethodPatch {
		s.handleUpdateDatabaseCompaction(w, r, name)
		return
	}
	if subPath == "seal" && r.Method == http.MethodPost {
		s.handleSealDatabase(w, r, name)
		return
	}
	if subPath == "unseal" && r.Method == http.MethodPost {
		s.handleUnsealDatabase(w, r, name)
		return
	}
	if subPath == "barrier/status" && r.Method == http.MethodGet {
		s.handleDatabaseBarrierStatus(w, r, name)
		return
	}
	if s.dispatchDatabaseHolder(w, r, name, subPath) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetDatabase(w, r, name)
	case http.MethodDelete:
		s.handleDropDatabase(w, r, name)
	default:
		w.Header().Set("Allow", "GET, DELETE, PATCH")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGetDatabase handles GET /v1/databases/{name} -- returns information
// about a specific database.
func (s *Server) handleGetDatabase(w http.ResponseWriter, r *http.Request, name string) {
	info, err := s.databaseSvc.GetForTenant(r.Context(), name)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, dbwire.Info(info))
}

// handleDropDatabase handles DELETE /v1/databases/{name} -- closes and
// removes a database, deleting its data.
func (s *Server) handleDropDatabase(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.databaseSvc.DropForTenant(r.Context(), name); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleUpdateDatabaseProtocol handles PATCH /v1/databases/{name}/protocol --
// updates the wire protocol configuration for a database.
func (s *Server) handleUpdateDatabaseProtocol(w http.ResponseWriter, r *http.Request, name string) {
	var req updateDatabaseProtocolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	info, err := s.databaseSvc.UpdateProtocolForTenant(r.Context(), name, database.ProtocolConfig{
		MySQL:      req.MySQL,
		PostgreSQL: req.PostgreSQL,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, dbwire.Info(info))
}

// handleUpdateDatabaseEtcd handles PATCH /v1/databases/{name}/etcd --
// updates the etcd wire protocol configuration for a database.
func (s *Server) handleUpdateDatabaseEtcd(w http.ResponseWriter, r *http.Request, name string) {
	var req updateDatabaseEtcdRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	cfg := &database.EtcdConfig{
		Enabled:             req.Enabled,
		ListenAddr:          req.ListenAddr,
		AdvertiseClientURLs: req.AdvertiseClientURLs,
	}

	info, err := s.databaseSvc.UpdateEtcdForTenant(r.Context(), name, cfg)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	if s.onEtcdUpdated != nil && cfg.Enabled {
		// The listener is for the database the update was applied to: the
		// caller's own, which another tenant may also hold a database named
		// after.
		if err := s.onEtcdUpdated(info.TenantID, name, cfg); err != nil {
			writeErrorJSON(w, mapHTTPError(err), err)
			return
		}
	}

	writeJSON(w, dbwire.Info(info))
}

// handleUpdateDatabaseRaft handles PATCH /v1/databases/{name}/raft --
// updates the Raft consensus configuration for a database.
func (s *Server) handleUpdateDatabaseRaft(w http.ResponseWriter, r *http.Request, name string) {
	var req updateDatabaseRaftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	raftCfg := raftConfigFromRequest(&raftConfigRequest{
		ClusterMode:    req.ClusterMode,
		ReplicaID:      req.ReplicaID,
		RaftAddress:    req.RaftAddress,
		Peers:          req.Peers,
		LocalNodeCount: req.LocalNodeCount,
		Discovery:      req.Discovery,
		Static:         req.Static,
		Multicast:      req.Multicast,
		DNS:            req.DNS,
		Token:          req.Token,
	})

	info, err := s.databaseSvc.UpdateRaftForTenant(r.Context(), name, raftCfg)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, dbwire.Info(info))
}

// handleScaleDatabase handles PATCH /v1/databases/{name}/scale -- changes
// the replication factor of an existing dedicated-raft database. When the
// local node is not the dedicated group's Raft leader and leader forwarding
// is configured, the request is transparently proxied to the leader (see
// tryForwardWrite), mirroring the KV PUT/DELETE handlers. The body is
// buffered up front (rather than decoded directly off r.Body) so the
// original bytes remain available for forwarding.
func (s *Server) handleScaleDatabase(w http.ResponseWriter, r *http.Request, name string) {
	body, ok := readBoundedBody(w, r, maxControlPlaneRequestBytes)
	if !ok {
		return
	}

	var req scaleDatabaseRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	info, err := s.databaseSvc.ScaleForTenant(r.Context(), name, req.TargetReplicas)
	if err != nil {
		if s.tryForwardWrite(w, r, body, err) {
			return
		}
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, dbwire.Info(info))
}

// handleUpdateDatabaseBarrier handles PATCH /v1/databases/{name}/barrier --
// updates the encryption barrier configuration for a database.
func (s *Server) handleUpdateDatabaseBarrier(w http.ResponseWriter, r *http.Request, name string) {
	var req updateDatabaseBarrierRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	cfg := &database.DatabaseBarrierConfig{
		Enabled:        req.Enabled,
		Mode:           req.Mode,
		SharesRequired: req.SharesRequired,
		SharesTotal:    req.SharesTotal,
	}

	info, err := s.databaseSvc.UpdateBarrierForTenant(r.Context(), name, cfg)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, dbwire.Info(info))
}

// handleUpdateDatabaseConnection handles PATCH /v1/databases/{name}/connection --
// updates the remote connection configuration for a database.
func (s *Server) handleUpdateDatabaseConnection(w http.ResponseWriter, r *http.Request, name string) {
	var req updateDatabaseConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	cfg := &database.DatabaseConnectionConfig{
		Transport: req.Transport,
		Address:   req.Address,
		CAFile:    req.CAFile,
		CertFile:  req.CertFile,
		KeyFile:   req.KeyFile,
	}

	info, err := s.databaseSvc.UpdateConnectionForTenant(r.Context(), name, cfg)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, dbwire.Info(info))
}

// handleUpdateDatabaseSQL handles PATCH /v1/databases/{name}/sql -- updates
// a database's SQL settings (its sql.concurrency).
func (s *Server) handleUpdateDatabaseSQL(w http.ResponseWriter, r *http.Request, name string) {
	var req updateDatabaseSQLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	info, err := s.databaseSvc.UpdateSQLForTenant(r.Context(), name, sqlConfigFromJSON(req.SQL))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, dbwire.Info(info))
}

// handleUpdateDatabaseCompaction handles PATCH /v1/databases/{name}/compaction
// -- sets a database's scheduled-compaction policy. The route requires
// mvcc:compact, and the service confines it to the caller's own databases.
func (s *Server) handleUpdateDatabaseCompaction(w http.ResponseWriter, r *http.Request, name string) {
	var req updateDatabaseCompactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	info, err := s.databaseSvc.UpdateCompactionForTenant(r.Context(), name, compactionConfigFromJSON(req.Compaction))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, dbwire.Info(info))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// raftConfigFromRequest converts a raftConfigRequest to a database.DatabaseRaftConfig.
func raftConfigFromRequest(req *raftConfigRequest) *database.DatabaseRaftConfig {
	raft := &database.DatabaseRaftConfig{
		ClusterMode:    database.RaftClusterMode(req.ClusterMode),
		ReplicaID:      req.ReplicaID,
		RaftAddress:    req.RaftAddress,
		Peers:          req.Peers,
		LocalNodeCount: req.LocalNodeCount,
		Discovery:      database.DiscoveryMethod(req.Discovery),
	}
	if req.Static != nil {
		raft.Static = &database.StaticDiscoveryConfig{
			Peers:        req.Static.Peers,
			SharedSecret: req.Static.SharedSecret,
		}
	}
	if req.Multicast != nil {
		raft.Multicast = &database.MulticastDiscoveryConfig{
			SharedSecret:     req.Multicast.SharedSecret,
			MulticastGroup:   req.Multicast.MulticastGroup,
			Interface:        req.Multicast.Interface,
			MinPeers:         req.Multicast.MinPeers,
			AnnounceInterval: req.Multicast.AnnounceInterval,
			DiscoverTimeout:  req.Multicast.DiscoverTimeout,
		}
	}
	if req.DNS != nil {
		raft.DNS = &database.DNSDiscoveryConfig{
			Service:       req.DNS.Service,
			Proto:         req.DNS.Proto,
			Domain:        req.DNS.Domain,
			LookupTimeout: req.DNS.LookupTimeout,
		}
	}
	if req.Token != nil {
		raft.Token = &database.TokenDiscoveryConfig{
			Endpoint:    req.Token.Endpoint,
			Token:       req.Token.Token,
			ClusterSize: req.Token.ClusterSize,
			Timeout:     req.Token.Timeout,
		}
	}
	return raft
}
