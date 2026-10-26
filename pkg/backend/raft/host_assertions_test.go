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

package raft_test

import (
	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/raftadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// HostAdapter and BootstrapAdapter are whole node hosts: each is the data host
// the data services take and the Raft-admin host the admin services take. The
// assertions live in an external test package, with no build constraint,
// because pkg/backend/raft does not import the service layer it serves.
var (
	_ service.Host   = (*raftbackend.HostAdapter)(nil)
	_ raftadmin.Host = (*raftbackend.HostAdapter)(nil)
	_ service.Host   = (*raftbackend.BootstrapAdapter)(nil)
	_ raftadmin.Host = (*raftbackend.BootstrapAdapter)(nil)
)
