// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// fakeMoveNodes is a MoveReplicaNodeSource double answering nodes or err.
type fakeMoveNodes struct {
	nodes []string
	err   error
	asked []string
}

func (f *fakeMoveNodes) MoveReplicaNodes(_ context.Context, tenantID string) ([]string, error) {
	f.asked = append(f.asked, tenantID)
	return f.nodes, f.err
}

// newMoveVolumeService returns a volume-entitled service holding the volume
// tenant "vtenant".
func newMoveVolumeService(t *testing.T) *TenantService {
	t.Helper()
	svc, _ := newVolumeService(t)
	if _, err := svc.CreateTenant(context.Background(), "vtenant", "Volume Tenant", IsolationVolume); err != nil {
		t.Fatalf("CreateTenant(volume): %v", err)
	}
	return svc
}

func moveVTenant() volume.MoveTenantRequest {
	return volume.MoveTenantRequest{TenantID: "vtenant", ToPool: "default"}
}

func TestAdmitTenantMove_TenantNotFound(t *testing.T) {
	svc, _ := newVolumeService(t)
	svc.SetMoveReplicaNodeSource(&fakeMoveNodes{nodes: []string{"1"}})
	_, err := svc.AdmitTenantMove(context.Background(), volume.MoveTenantRequest{TenantID: "ghost", ToPool: "default"})
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("err = %v, want ErrTenantNotFound", err)
	}
}

func TestAdmitTenantMove_PrefixTenantRefused(t *testing.T) {
	svc, _ := newVolumeService(t)
	src := &fakeMoveNodes{nodes: []string{"1"}}
	svc.SetMoveReplicaNodeSource(src)
	if _, err := svc.CreateTenant(context.Background(), "ptenant", "Prefix Tenant", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(prefix): %v", err)
	}
	_, err := svc.AdmitTenantMove(context.Background(), volume.MoveTenantRequest{TenantID: "ptenant", ToPool: "default"})
	var conflict *volume.MoveConflictError
	if !errors.As(err, &conflict) || conflict.TenantID != "ptenant" {
		t.Fatalf("err = %v, want *volume.MoveConflictError for ptenant", err)
	}
	if len(src.asked) != 0 {
		t.Fatal("replica nodes asked for a refused tenant")
	}
}

func TestAdmitTenantMove_NotEntitledToDestination(t *testing.T) {
	svc := newMoveVolumeService(t)
	src := &fakeMoveNodes{nodes: []string{"1"}}
	svc.SetMoveReplicaNodeSource(src)
	req := moveVTenant()
	req.ToPool = "bulk"
	_, err := svc.AdmitTenantMove(context.Background(), req)
	var ent *quota.EntitlementError
	if !errors.As(err, &ent) || ent.Feature != "volume_pool:bulk" {
		t.Fatalf("err = %v, want *quota.EntitlementError for volume_pool:bulk", err)
	}
	if len(src.asked) != 0 {
		t.Fatal("replica nodes asked for an unentitled move")
	}
}

func TestAdmitTenantMove_NoSource(t *testing.T) {
	svc := newMoveVolumeService(t)
	var notImpl *volume.MoveNotImplementedError
	if _, err := svc.AdmitTenantMove(context.Background(), moveVTenant()); !errors.As(err, &notImpl) {
		t.Fatalf("err = %v, want *volume.MoveNotImplementedError", err)
	}
	svc.SetMoveReplicaNodeSource(&fakeMoveNodes{nodes: []string{"1"}})
	svc.SetMoveReplicaNodeSource(nil)
	if _, err := svc.AdmitTenantMove(context.Background(), moveVTenant()); !errors.As(err, &notImpl) {
		t.Fatalf("after unwiring err = %v, want *volume.MoveNotImplementedError", err)
	}
}

func TestAdmitTenantMove_SourceError(t *testing.T) {
	svc := newMoveVolumeService(t)
	boom := errors.New("membership unavailable")
	svc.SetMoveReplicaNodeSource(&fakeMoveNodes{err: boom})
	if _, err := svc.AdmitTenantMove(context.Background(), moveVTenant()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the source's error", err)
	}
}

func TestAdmitTenantMove_NoReplicaNode(t *testing.T) {
	svc := newMoveVolumeService(t)
	svc.SetMoveReplicaNodeSource(&fakeMoveNodes{})
	var conflict *volume.MoveConflictError
	if _, err := svc.AdmitTenantMove(context.Background(), moveVTenant()); !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *volume.MoveConflictError", err)
	}
}

func TestAdmitTenantMove_VolumeTenantAdmitted(t *testing.T) {
	svc := newMoveVolumeService(t)
	src := &fakeMoveNodes{nodes: []string{"1", "2", "3"}}
	svc.SetMoveReplicaNodeSource(src)
	nodes, err := svc.AdmitTenantMove(context.Background(), moveVTenant())
	if err != nil {
		t.Fatalf("AdmitTenantMove: %v", err)
	}
	if !reflect.DeepEqual(nodes, []string{"1", "2", "3"}) {
		t.Fatalf("nodes = %v, want [1 2 3]", nodes)
	}
	if !reflect.DeepEqual(src.asked, []string{"vtenant"}) {
		t.Fatalf("source asked for %v, want [vtenant]", src.asked)
	}
}

func TestAdmitTenantMove_RaftTenantAdmitted(t *testing.T) {
	svc, _ := newRaftService(t)
	if _, err := svc.CreateTenant(context.Background(), "rtenant", "Raft Tenant", IsolationRaft); err != nil {
		t.Fatalf("CreateTenant(raft): %v", err)
	}
	svc.SetMoveReplicaNodeSource(&fakeMoveNodes{nodes: []string{"7"}})
	nodes, err := svc.AdmitTenantMove(context.Background(), volume.MoveTenantRequest{TenantID: "rtenant", ToPool: "default"})
	if err != nil {
		t.Fatalf("AdmitTenantMove: %v", err)
	}
	if !reflect.DeepEqual(nodes, []string{"7"}) {
		t.Fatalf("nodes = %v, want [7]", nodes)
	}
}
