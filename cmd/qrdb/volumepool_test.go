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

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// executeVolumePoolCmd installs an embedded SDK client over svc as the
// package-level client and runs the volume-pool command tree in format,
// returning captured stdout.
func executeVolumePoolCmd(t *testing.T, svc volume.Service, format string, args ...string) (string, error) {
	t.Helper()

	tr, err := embedded.New(&noopKVService{}, newMockAdminService(), embedded.WithVolumeService(svc))
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	sdkClient, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = sdkClient.Close() })

	oldClient, oldWriter, oldFormat := client, outputWriter, outputFormat
	t.Cleanup(func() {
		client, outputWriter, outputFormat = oldClient, oldWriter, oldFormat
	})
	client = sdkClient
	outputFormat = format
	var buf bytes.Buffer
	outputWriter = &buf

	cmd := newVolumePoolCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	execErr := cmd.ExecuteContext(context.Background())
	return buf.String(), execErr
}

var volumePoolCLITime = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func samplePoolViews() []volume.PoolView {
	return []volume.PoolView{{
		Pool:        volume.Pool{Name: "fast", Provider: "dir", CapacityBytes: 2 << 30, ReservedBytes: 1 << 20, Overcommit: 1.5},
		UsedBytes:   4096,
		TenantCount: 3,
		NodeCounts:  volume.NodeHealthCounts{Ready: 2, Locked: 1, Unavailable: 0, Unmapped: 0},
	}}
}

func TestVolumePoolList_Table(t *testing.T) {
	svc := &volumetest.Service{T: t, ListPoolsFn: func(context.Context) ([]volume.PoolView, error) { return samplePoolViews(), nil }}
	out, err := executeVolumePoolCmd(t, svc, "table", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"NAME", "NODES (ready/locked/unavail/unmapped)", "fast", "dir", "2.00 GB", "1.00 MB", "4.00 KB", "2/1/0/0"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestVolumePoolList_JSONAndYAML(t *testing.T) {
	svc := &volumetest.Service{T: t, ListPoolsFn: func(context.Context) ([]volume.PoolView, error) { return samplePoolViews(), nil }}

	out, err := executeVolumePoolCmd(t, svc, "json", "list")
	if err != nil {
		t.Fatalf("list json: %v", err)
	}
	var pools []struct {
		Name        string `json:"name"`
		TenantCount int    `json:"tenantCount"`
	}
	if err := json.Unmarshal([]byte(out), &pools); err != nil || len(pools) != 1 || pools[0].Name != "fast" || pools[0].TenantCount != 3 {
		t.Fatalf("json = %q (%+v, %v)", out, pools, err)
	}

	out, err = executeVolumePoolCmd(t, svc, "yaml", "list")
	if err != nil || !strings.Contains(out, "name: fast") {
		t.Fatalf("yaml = %q err = %v", out, err)
	}
}

func TestVolumePoolList_ServiceErrorPropagates(t *testing.T) {
	boom := &volume.PoolUnavailableError{Pool: "fast"}
	svc := &volumetest.Service{T: t, ListPoolsFn: func(context.Context) ([]volume.PoolView, error) { return nil, boom }}
	if _, err := executeVolumePoolCmd(t, svc, "table", "list"); !errors.As(err, new(*volume.PoolUnavailableError)) {
		t.Fatalf("err = %v, want PoolUnavailableError", err)
	}
}

// TestVolumePoolGet_TableHidesStaleNodeNumbers proves a no_report row is
// printed as "no report" with dashes, never with the node's last numbers.
func TestVolumePoolGet_TableHidesStaleNodeNumbers(t *testing.T) {
	svc := &volumetest.Service{T: t, GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
		return &volume.PoolDetail{
			PoolView: volume.PoolView{Pool: volume.Pool{Name: name, Provider: "dir", Description: "nvme tier", Overcommit: 1.25}, TenantCount: 1},
			NodeReports: []volume.NodeReportRow{
				{NodeID: "node-a", State: "ready", SizeBytes: 3 << 30, FreeBytes: 1 << 30, Detail: "mounted", ReportedAt: volumePoolCLITime},
				{NodeID: "node-b", State: volume.NodeStateNoReport, SizeBytes: 777777777, Detail: "lease expired"},
			},
		}, nil
	}}
	out, err := executeVolumePoolCmd(t, svc, "table", "get", "fast")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for _, want := range []string{"Name:       fast", "Description: nvme tier", "Overcommit: 1.25", "node-a", "3.00 GB", "mounted", "node-b", "no report", "lease expired"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "741.") || strings.Contains(out, strconv.Itoa(777777777)) {
		t.Fatalf("stale size leaked for a no_report node:\n%s", out)
	}
}

func TestVolumePoolGet_NoNodeReportsAndJSON(t *testing.T) {
	svc := &volumetest.Service{T: t, GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
		if name == "gone" {
			return nil, &volume.PoolNotFoundError{Pool: name}
		}
		return &volume.PoolDetail{PoolView: volume.PoolView{Pool: volume.Pool{Name: name, Provider: "dir"}}}, nil
	}}
	out, err := executeVolumePoolCmd(t, svc, "table", "get", "fast")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Contains(out, "NODE") || strings.Contains(out, "Description:") {
		t.Fatalf("empty description and node table must be omitted:\n%s", out)
	}

	out, err = executeVolumePoolCmd(t, svc, "json", "get", "fast")
	if err != nil || !strings.Contains(out, `"name": "fast"`) {
		t.Fatalf("json = %q err = %v", out, err)
	}
	out, err = executeVolumePoolCmd(t, svc, "yaml", "get", "fast")
	if err != nil || !strings.Contains(out, "name: fast") {
		t.Fatalf("yaml = %q err = %v", out, err)
	}

	if _, err := executeVolumePoolCmd(t, svc, "table", "get", "gone"); !errors.As(err, new(*volume.PoolNotFoundError)) {
		t.Fatalf("err = %v, want PoolNotFoundError", err)
	}
}

func TestVolumePoolCreate_ParsesCapacityUnits(t *testing.T) {
	var got volume.CreatePoolRequest
	svc := &volumetest.Service{T: t, CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
		got = req
		return &volume.Pool{Name: req.Name, Provider: req.Provider, CapacityBytes: req.CapacityBytes, Overcommit: req.Overcommit}, nil
	}}
	out, err := executeVolumePoolCmd(t, svc, "table", "create", "fast",
		"--provider", "dir", "--description", "nvme", "--capacity", "2TiB", "--overcommit", "1.5")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := volume.CreatePoolRequest{Name: "fast", Provider: "dir", Description: "nvme", CapacityBytes: 2 << 40, Overcommit: 1.5}
	if got != want {
		t.Fatalf("service saw %+v, want %+v", got, want)
	}
	if !strings.Contains(out, "Name:       fast") || !strings.Contains(out, "Overcommit: 1.50") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestVolumePoolCreate_RejectsBadCapacityBeforeCallingService(t *testing.T) {
	svc := &volumetest.Service{T: t} // CreatePool unset: reaching it fails the test
	_, err := executeVolumePoolCmd(t, svc, "table", "create", "fast", "--capacity", "lots")
	var bse *InvalidByteSizeError
	if !errors.As(err, &bse) || bse.Value != "lots" {
		t.Fatalf("err = %v, want InvalidByteSizeError(lots)", err)
	}
	if !strings.Contains(err.Error(), `"lots"`) || errors.Unwrap(err) == nil {
		t.Fatalf("error %q must quote the value and wrap its cause", err)
	}
}

func TestVolumePoolCreate_ServiceErrorPropagates(t *testing.T) {
	svc := &volumetest.Service{T: t, CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
		return nil, &volume.PoolAlreadyExistsError{Pool: req.Name}
	}}
	if _, err := executeVolumePoolCmd(t, svc, "table", "create", "fast"); !errors.As(err, new(*volume.PoolAlreadyExistsError)) {
		t.Fatalf("err = %v, want PoolAlreadyExistsError", err)
	}
}

// TestVolumePoolUpdate_OnlyChangedFlagsAreSent proves an unset flag leaves
// the property alone while an explicitly set zero is forwarded.
func TestVolumePoolUpdate_OnlyChangedFlagsAreSent(t *testing.T) {
	var got volume.UpdatePoolRequest
	svc := &volumetest.Service{T: t, UpdatePoolFn: func(_ context.Context, name string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
		if name == "gone" {
			return nil, &volume.PoolNotFoundError{Pool: name}
		}
		got = req
		return &volume.Pool{Name: name, Provider: "dir", Overcommit: 1}, nil
	}}

	if _, err := executeVolumePoolCmd(t, svc, "json", "update", "fast", "--capacity", "0"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.CapacityBytes == nil || *got.CapacityBytes != 0 || got.Description != nil || got.Overcommit != nil {
		t.Fatalf("capacity-only update = %+v", got)
	}

	if _, err := executeVolumePoolCmd(t, svc, "yaml", "update", "fast", "--description", "ssd", "--overcommit", "2"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.CapacityBytes != nil || got.Description == nil || *got.Description != "ssd" || got.Overcommit == nil || *got.Overcommit != 2 {
		t.Fatalf("description+overcommit update = %+v", got)
	}

	if _, err := executeVolumePoolCmd(t, svc, "table", "update", "fast", "--capacity", "1.5XB"); !errors.As(err, new(*InvalidByteSizeError)) {
		t.Fatalf("bad capacity: err = %v, want InvalidByteSizeError", err)
	}
	if _, err := executeVolumePoolCmd(t, svc, "table", "update", "gone"); !errors.As(err, new(*volume.PoolNotFoundError)) {
		t.Fatalf("missing pool: err = %v, want PoolNotFoundError", err)
	}
}

func TestVolumePoolDelete(t *testing.T) {
	var deleted []string
	svc := &volumetest.Service{T: t, DeletePoolFn: func(_ context.Context, name string) error {
		if name == "busy" {
			return &volume.PoolInUseError{Pool: name, Tenants: []string{"t1"}}
		}
		deleted = append(deleted, name)
		return nil
	}}
	if _, err := executeVolumePoolCmd(t, svc, "table", "delete", "fast"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var inUse *volume.PoolInUseError
	if _, err := executeVolumePoolCmd(t, svc, "table", "delete", "busy"); !errors.As(err, &inUse) || inUse.Tenants[0] != "t1" {
		t.Fatalf("err = %v, want PoolInUseError naming t1", err)
	}
	if len(deleted) != 1 || deleted[0] != "fast" {
		t.Fatalf("deleted = %v", deleted)
	}
}

func TestVolumePoolTenants_TablePrintsNextPageToken(t *testing.T) {
	svc := &volumetest.Service{T: t, ListAssignmentsFn: func(_ context.Context, pool, token string, size int) (*volume.AssignmentPage, error) {
		if pool != "fast" || token != "t1" || size != 2 {
			t.Fatalf("ListAssignments(%q, %q, %d)", pool, token, size)
		}
		return &volume.AssignmentPage{
			Assignments:   []volume.Assignment{{TenantID: "t2", ReservedBytes: 2048}, {TenantID: "t3", ReservedBytes: 10}},
			NextPageToken: "t3",
		}, nil
	}}
	out, err := executeVolumePoolCmd(t, svc, "table", "tenants", "fast", "--page-size", "2", "--page-token", "t1")
	if err != nil {
		t.Fatalf("tenants: %v", err)
	}
	for _, want := range []string{"TENANT_ID", "t2", "2.00 KB", "t3", "10 B", "next_page_token: t3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestVolumePoolTenants_LastPageAndStructuredFormats(t *testing.T) {
	svc := &volumetest.Service{T: t, ListAssignmentsFn: func(_ context.Context, pool, _ string, _ int) (*volume.AssignmentPage, error) {
		if pool == "gone" {
			return nil, &volume.PoolNotFoundError{Pool: pool}
		}
		return &volume.AssignmentPage{Assignments: []volume.Assignment{{TenantID: "t9", ReservedBytes: 1}}}, nil
	}}
	out, err := executeVolumePoolCmd(t, svc, "table", "tenants", "fast")
	if err != nil || strings.Contains(out, "next_page_token") {
		t.Fatalf("last page must not print a token: %q err = %v", out, err)
	}
	out, err = executeVolumePoolCmd(t, svc, "json", "tenants", "fast")
	if err != nil || !strings.Contains(out, `"t9"`) {
		t.Fatalf("json = %q err = %v", out, err)
	}
	out, err = executeVolumePoolCmd(t, svc, "yaml", "tenants", "fast")
	if err != nil || !strings.Contains(out, "t9") {
		t.Fatalf("yaml = %q err = %v", out, err)
	}
	if _, err := executeVolumePoolCmd(t, svc, "table", "tenants", "gone"); !errors.As(err, new(*volume.PoolNotFoundError)) {
		t.Fatalf("err = %v, want PoolNotFoundError", err)
	}
}

func TestParseByteSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"0", 0},
		{"4096", 4096},
		{"512B", 512},
		{"1KiB", 1 << 10},
		{"1.5 MiB", 3 << 19},
		{"500gib", 500 << 30},
		{"2TiB", 2 << 40},
		{"1PiB", 1 << 50},
	}
	for _, tc := range cases {
		got, err := parseByteSize(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("parseByteSize(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"lots", "GiB", "1.2.3MiB", "12XB"} {
		if _, err := parseByteSize(bad); !errors.As(err, new(*InvalidByteSizeError)) {
			t.Errorf("parseByteSize(%q) err = %v, want InvalidByteSizeError", bad, err)
		}
	}
}
