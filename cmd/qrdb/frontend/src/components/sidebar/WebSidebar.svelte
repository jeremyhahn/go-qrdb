<!--
  Copyright 2026 Jeremy Hahn
  SPDX-License-Identifier: Apache-2.0

  Licensed under the Apache License, Version 2.0 (the "License");
  you may not use this file except in compliance with the License.
  You may obtain a copy of the License at

      http://www.apache.org/licenses/LICENSE-2.0

  Unless required by applicable law or agreed to in writing, software
  distributed under the License is distributed on an "AS IS" BASIS,
  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
  See the License for the specific language governing permissions and
  limitations under the License.
-->

<!--
  WebSidebar renders the sidebar for web mode where there is exactly one
  implicit server — no connection management, no server tree, no Disconnect
  button. It is structurally equivalent to one expanded ServerNode without
  the server header row.

  The Databases and Server Admin sections are shown as top-level groups,
  matching the hierarchy a user would see inside an expanded server node in
  desktop mode.
-->
<script lang="ts">
  import type { DatabaseInfo } from "@qrdb/sdk";
  import { Database, Shield } from "lucide-svelte";
  import DatabaseList from "./DatabaseList.svelte";
  import AdminNav from "./AdminNav.svelte";
  import { connManagerStore } from "$stores/connection-manager.svelte.ts";

  // ---------------------------------------------------------------------------
  // Props
  // ---------------------------------------------------------------------------

  interface Props {
    /** A stable identifier for the implicit web server (typically "web"). */
    profileId: string;
    /** Databases to display. */
    databases: readonly DatabaseInfo[];
    /** Whether the database list is currently loading. */
    dbLoading?: boolean;
    /** Whether the sidebar is in collapsed (icon-rail) mode. */
    collapsed?: boolean;
  }

  let {
    profileId,
    databases,
    dbLoading = false,
    collapsed = false,
  }: Props = $props();

  // Web mode always treats the single server as "active".
  const WEB_IS_ACTIVE = true;

  // The hosting server's topology mode, detected from its live /admin/topology
  // when the web profile connects (see connection-manager.detectWebServerMode).
  // Drives whether AdminNav exposes the Raft cluster surfaces (Cluster /
  // Topology). Defaults to standalone until detection completes.
  const serverMode = $derived(
    connManagerStore.activeServerMode === "raft" ? "raft" : "standalone",
  );
</script>

<!-- Databases section -->
{#if !collapsed}
  <div class="mt-1 mb-1 flex items-center gap-1.5 px-2">
    <Database class="h-3 w-3 text-base-content/40" />
    <span class="text-base-content/40 text-[10px] font-semibold uppercase tracking-wider">
      Databases
    </span>
  </div>
{:else}
  <div class="my-1 border-t border-base-content/10"></div>
{/if}

<DatabaseList
  {profileId}
  {databases}
  loading={dbLoading}
  isActive={WEB_IS_ACTIVE}
  {collapsed}
/>

<!-- Server Admin section -->
{#if !collapsed}
  <div class="mt-3 mb-1 flex items-center gap-1.5 px-2">
    <Shield class="h-3 w-3 text-base-content/40" />
    <span class="text-base-content/40 text-[10px] font-semibold uppercase tracking-wider">
      Server Admin
    </span>
  </div>
{:else}
  <div class="my-1 border-t border-base-content/10"></div>
{/if}

<AdminNav {profileId} {collapsed} {serverMode} />
