// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func seedAssignments(t *testing.T, cat *Catalog, pool string, n int) {
	t.Helper()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: pool, CapacityBytes: int64(n) * 1000}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	for i := 0; i < n; i++ {
		tenantID := fmt.Sprintf("%s-tenant-%03d", pool, i)
		if _, err := cat.Reserve(ctx, tenantID, pool, tenantID, 10); err != nil {
			t.Fatalf("Reserve(%s): %v", tenantID, err)
		}
	}
}

func TestCatalog_ListAssignments_SinglePage(t *testing.T) {
	cat, _, _ := newTestCatalog()
	seedAssignments(t, cat, "fast", 3)

	page, err := cat.ListAssignments(context.Background(), "fast", "", 10)
	if err != nil {
		t.Fatalf("ListAssignments: %v", err)
	}
	if len(page.Assignments) != 3 {
		t.Fatalf("ListAssignments returned %d assignments, want 3", len(page.Assignments))
	}
	if page.NextPageToken != "" {
		t.Fatalf("ListAssignments.NextPageToken = %q, want empty on the last page", page.NextPageToken)
	}
}

func TestCatalog_ListAssignments_PagesInOrder(t *testing.T) {
	cat, _, _ := newTestCatalog()
	seedAssignments(t, cat, "fast", 5)
	ctx := context.Background()

	var seen []string
	token := ""
	for {
		page, err := cat.ListAssignments(ctx, "fast", token, 2)
		if err != nil {
			t.Fatalf("ListAssignments: %v", err)
		}
		for _, a := range page.Assignments {
			seen = append(seen, a.TenantID)
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	want := []string{"fast-tenant-000", "fast-tenant-001", "fast-tenant-002", "fast-tenant-003", "fast-tenant-004"}
	if len(seen) != len(want) {
		t.Fatalf("paged through %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("paged through %v, want %v", seen, want)
		}
	}
}

func TestCatalog_ListAssignments_FiltersByPool(t *testing.T) {
	cat, _, _ := newTestCatalog()
	seedAssignments(t, cat, "fast", 2)
	seedAssignments(t, cat, "bulk", 3)

	page, err := cat.ListAssignments(context.Background(), "bulk", "", 10)
	if err != nil {
		t.Fatalf("ListAssignments: %v", err)
	}
	if len(page.Assignments) != 3 {
		t.Fatalf("ListAssignments(bulk) returned %d, want 3", len(page.Assignments))
	}
	for _, a := range page.Assignments {
		if a.Pool != "bulk" {
			t.Fatalf("ListAssignments(bulk) returned an assignment for pool %q", a.Pool)
		}
	}
}

func TestCatalog_ListAssignments_EmptyPoolListsEverything(t *testing.T) {
	cat, _, _ := newTestCatalog()
	seedAssignments(t, cat, "fast", 2)
	seedAssignments(t, cat, "bulk", 3)

	page, err := cat.ListAssignments(context.Background(), "", "", 10)
	if err != nil {
		t.Fatalf("ListAssignments: %v", err)
	}
	if len(page.Assignments) != 5 {
		t.Fatalf("ListAssignments(\"\") returned %d, want 5", len(page.Assignments))
	}
}

func TestCatalog_ListAssignments_InvalidPageSize(t *testing.T) {
	cat, _, _ := newTestCatalog()
	_, err := cat.ListAssignments(context.Background(), "fast", "", -1)
	var target *InvalidPageRequestError
	if !errors.As(err, &target) {
		t.Fatalf("ListAssignments(negative page size) error = %v, want *InvalidPageRequestError", err)
	}
}

func TestCatalog_ListAssignments_InvalidPageToken(t *testing.T) {
	cat, _, _ := newTestCatalog()
	_, err := cat.ListAssignments(context.Background(), "fast", "not-valid-base64!!", 10)
	var target *InvalidPageRequestError
	if !errors.As(err, &target) {
		t.Fatalf("ListAssignments(invalid page token) error = %v, want *InvalidPageRequestError", err)
	}
}

func TestResolveAssignmentPageSize(t *testing.T) {
	cases := []struct {
		in      int
		want    int
		wantErr bool
	}{
		{0, DefaultAssignmentPageSize, false},
		{50, 50, false},
		{MaxAssignmentPageSize + 1, MaxAssignmentPageSize, false},
		{-1, 0, true},
	}
	for _, c := range cases {
		got, err := resolveAssignmentPageSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("resolveAssignmentPageSize(%d) should have errored", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("resolveAssignmentPageSize(%d): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("resolveAssignmentPageSize(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAssignmentPageToken_RoundTrips(t *testing.T) {
	encoded := encodeAssignmentPageToken("tenant-042")
	decoded, err := decodeAssignmentPageToken(encoded)
	if err != nil {
		t.Fatalf("decodeAssignmentPageToken: %v", err)
	}
	if decoded != "tenant-042" {
		t.Fatalf("decodeAssignmentPageToken(encode(x)) = %q, want %q", decoded, "tenant-042")
	}
}

func TestDecodeAssignmentPageToken_Empty(t *testing.T) {
	decoded, err := decodeAssignmentPageToken("")
	if err != nil {
		t.Fatalf("decodeAssignmentPageToken(\"\"): %v", err)
	}
	if decoded != "" {
		t.Fatalf("decodeAssignmentPageToken(\"\") = %q, want empty", decoded)
	}
}
