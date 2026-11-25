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

/**
 * Group wire mapping shared by every transport. The REST groups handlers and
 * the desktop GroupBinding both speak the Go transport group types, whose
 * JSON keys are camelCase with Go-style ID suffixes (tenantID, roleIDs,
 * userID, groupID); the SDK's Group types use tenantId, roleIds and userId.
 * Requests are built in wire form here and sent without re-casing any key.
 */

import type {
    CreateGroupRequest,
    Group,
    GroupMember,
    ListGroupsRequest,
    ListGroupsResponse,
    UpdateGroupRequest,
} from './types.js';

/** Group wire shape (sdk/go/transport.Group). */
export interface GroupWire {
    readonly id?: string;
    readonly tenantID?: string;
    readonly name?: string;
    readonly description?: string;
    readonly roleIDs?: readonly string[] | null;
    readonly createdAt?: string;
    readonly updatedAt?: string;
}

/**
 * Group member wire shape: userID is the uint64 user ID as a decimal string,
 * never a JSON number (a number above 2^53-1 would round to another user).
 */
export interface GroupMemberWire {
    readonly userID?: string;
    readonly username?: string;
    readonly tenantID?: string;
}

/** Group page wire shape (sdk/go/transport.ListGroupsResponse). */
export interface ListGroupsWire {
    readonly groups?: readonly GroupWire[] | null;
    readonly total?: number;
    readonly hasMore?: boolean;
}

/** Maps a wire group onto the SDK's Group. */
export function groupFromWire(w: GroupWire): Group {
    return {
        id: w.id ?? '',
        tenantId: w.tenantID ?? '',
        name: w.name ?? '',
        description: w.description ?? '',
        roleIds: [...(w.roleIDs ?? [])],
        createdAt: w.createdAt ?? '',
        updatedAt: w.updatedAt ?? '',
    };
}

/** Maps a wire group member onto the SDK's GroupMember. */
export function groupMemberFromWire(w: GroupMemberWire): GroupMember {
    return {
        userId: w.userID ?? '',
        username: w.username ?? '',
        tenantId: w.tenantID ?? '',
    };
}

/** Maps a wire group page onto the SDK's ListGroupsResponse. */
export function groupPageFromWire(w: ListGroupsWire): ListGroupsResponse {
    return {
        groups: (w.groups ?? []).map(groupFromWire),
        total: w.total ?? 0,
        hasMore: w.hasMore ?? false,
    };
}

/** Builds the wire body of a group create; unset fields are omitted. */
export function createGroupWire(req: CreateGroupRequest): Record<string, unknown> {
    const body: Record<string, unknown> = { name: req.name };
    if (req.tenantId !== undefined) body['tenantID'] = req.tenantId;
    if (req.description !== undefined) body['description'] = req.description;
    if (req.roleIds !== undefined) body['roleIDs'] = [...req.roleIds];
    return body;
}

/**
 * Builds the wire body of a partial group update (without the id, which
 * REST carries in the path); unset fields are omitted and left unchanged.
 */
export function updateGroupWire(req: UpdateGroupRequest): Record<string, unknown> {
    const body: Record<string, unknown> = {};
    if (req.name !== undefined) body['name'] = req.name;
    if (req.description !== undefined) body['description'] = req.description;
    if (req.roleIds !== undefined) body['roleIDs'] = [...req.roleIds];
    return body;
}

/** Builds the wire form of a group page request; unset fields are omitted. */
export function listGroupsWire(req?: ListGroupsRequest): Record<string, unknown> {
    const body: Record<string, unknown> = {};
    if (req?.tenantId !== undefined) body['tenantID'] = req.tenantId;
    if (req?.page !== undefined) body['page'] = req.page;
    if (req?.pageSize !== undefined) body['pageSize'] = req.pageSize;
    return body;
}
