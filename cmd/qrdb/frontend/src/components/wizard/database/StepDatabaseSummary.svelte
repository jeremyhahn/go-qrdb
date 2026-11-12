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
<script lang="ts">
  import { dbWizardStore } from "$stores/database-wizard.svelte.ts";

  let { onNext, onBack }: { onNext: () => void; onBack: () => void } = $props();

  /** Human-readable engine labels. */
  const engineLabels: Readonly<Record<string, string>> = {
    kv: "Key-Value",
    timeseries: "TimeSeries",
    vector: "Vector",
  } as const;

  /** Human-readable backend labels. */
  const backendLabels: Readonly<Record<string, string>> = {
    memory: "Memory",
    file: "File",
    pebble: "Pebble",
  } as const;

  /** Human-readable cluster mode labels. */
  const clusterModeLabels: Readonly<Record<string, string>> = {
    none: "Standalone",
    local: "Local Development",
    distributed: "Distributed",
  } as const;
</script>

<div class="flex flex-col items-center gap-6">
  <div class="text-center">
    <h2 class="text-2xl font-bold">Review Configuration</h2>
    <p class="text-base-content/70 mt-2">
      Verify your database settings before creation.
    </p>
  </div>

  <div class="w-full max-w-2xl space-y-4">
    <!-- Database identity -->
    <div class="bg-base-200 rounded-lg p-4">
      <h3
        class="mb-3 text-sm font-semibold uppercase tracking-wide text-primary"
      >
        Database
      </h3>
      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <span class="text-base-content/70">Name</span>
          <span class="font-mono text-sm">{dbWizardStore.name}</span>
        </div>
        <div class="flex items-center justify-between">
          <span class="text-base-content/70">Engine</span>
          <span class="font-medium">
            {engineLabels[dbWizardStore.engine] ?? dbWizardStore.engine}
          </span>
        </div>
        <div class="flex items-center justify-between">
          <span class="text-base-content/70">Mode</span>
          <span class="badge badge-sm badge-outline">
            {dbWizardStore.mode === "embedded" ? "Embedded" : "Remote"}
          </span>
        </div>
      </div>
    </div>

    <!-- Storage (embedded only) -->
    {#if dbWizardStore.mode === "embedded"}
      <div class="bg-base-200 rounded-lg p-4">
        <h3
          class="mb-3 text-sm font-semibold uppercase tracking-wide text-primary"
        >
          Storage
        </h3>
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <span class="text-base-content/70">Backend</span>
            <span class="font-medium">
              {backendLabels[dbWizardStore.backend] ?? dbWizardStore.backend}
            </span>
          </div>
          {#if dbWizardStore.dataDir}
            <div class="flex items-center justify-between">
              <span class="text-base-content/70">Data Directory</span>
              <span class="font-mono text-sm">{dbWizardStore.dataDir}</span>
            </div>
          {/if}
        </div>
      </div>

      <!-- Raft -->
      {#if dbWizardStore.raftConfig !== null}
        <div class="bg-base-200 rounded-lg p-4">
          <h3
            class="mb-3 text-sm font-semibold uppercase tracking-wide text-primary"
          >
            Raft Cluster
          </h3>
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <span class="text-base-content/70">Cluster Mode</span>
              <span class="font-medium">
                {clusterModeLabels[dbWizardStore.raftConfig.clusterMode] ??
                  dbWizardStore.raftConfig.clusterMode}
              </span>
            </div>
            {#if dbWizardStore.raftConfig.clusterMode === "local"}
              <div class="flex items-center justify-between">
                <span class="text-base-content/70">Local Nodes</span>
                <span class="font-medium">
                  {dbWizardStore.raftConfig.localNodeCount}
                </span>
              </div>
            {/if}
            {#if dbWizardStore.raftConfig.clusterMode === "distributed"}
              <div class="flex items-center justify-between">
                <span class="text-base-content/70">Replica ID</span>
                <span class="font-mono text-sm">
                  {dbWizardStore.raftConfig.replicaId}
                </span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-base-content/70">Raft Address</span>
                <span class="font-mono text-sm">
                  {dbWizardStore.raftConfig.raftAddress}
                </span>
              </div>
              <div class="flex items-center justify-between">
                <span class="text-base-content/70">Peers</span>
                <span class="font-mono text-sm">
                  {dbWizardStore.raftConfig.peers.length} configured
                </span>
              </div>
            {/if}
          </div>
        </div>
      {/if}
    {/if}

    <!-- Connection (remote only) -->
    {#if dbWizardStore.mode === "remote" && dbWizardStore.connectionConfig !== null}
      <div class="bg-base-200 rounded-lg p-4">
        <h3
          class="mb-3 text-sm font-semibold uppercase tracking-wide text-primary"
        >
          Connection
        </h3>
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <span class="text-base-content/70">Transport</span>
            <span class="font-medium">
              {dbWizardStore.connectionConfig.transport}
            </span>
          </div>
          <div class="flex items-center justify-between">
            <span class="text-base-content/70">Address</span>
            <span class="font-mono text-sm">
              {dbWizardStore.connectionConfig.address}
            </span>
          </div>
          {#if dbWizardStore.connectionConfig.caFile}
            <div class="flex items-center justify-between">
              <span class="text-base-content/70">CA Certificate</span>
              <span class="font-mono text-sm">
                {dbWizardStore.connectionConfig.caFile}
              </span>
            </div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- SQL Wire Protocols -->
    {#if dbWizardStore.sqlMySQL || dbWizardStore.sqlPostgreSQL}
      <div class="bg-base-200 rounded-lg p-4">
        <h3
          class="mb-3 text-sm font-semibold uppercase tracking-wide text-primary"
        >
          SQL Wire Protocols
        </h3>
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <span class="text-base-content/70">MySQL</span>
            <span
              class="badge badge-sm {dbWizardStore.sqlMySQL
                ? 'badge-success'
                : 'badge-ghost'}"
            >
              {dbWizardStore.sqlMySQL ? "Enabled" : "Disabled"}
            </span>
          </div>
          <div class="flex items-center justify-between">
            <span class="text-base-content/70">PostgreSQL</span>
            <span
              class="badge badge-sm {dbWizardStore.sqlPostgreSQL
                ? 'badge-success'
                : 'badge-ghost'}"
            >
              {dbWizardStore.sqlPostgreSQL ? "Enabled" : "Disabled"}
            </span>
          </div>
        </div>
      </div>
    {/if}

    <!-- Encryption barrier -->
    <div class="bg-base-200 rounded-lg p-4">
      <h3
        class="mb-3 text-sm font-semibold uppercase tracking-wide text-primary"
      >
        Encryption Barrier
      </h3>
      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <span class="text-base-content/70">Mode</span>
          <span class="font-medium">
            {#if dbWizardStore.sealMode === "passphrase"}
              <span class="badge badge-success badge-sm">Passphrase</span>
            {:else if dbWizardStore.sealMode === "shamir"}
              <span class="badge badge-success badge-sm">Shamir</span>
            {:else}
              <span class="badge badge-warning badge-sm">Disabled</span>
            {/if}
          </span>
        </div>
        {#if dbWizardStore.sealMode === "passphrase"}
          <div class="flex items-center justify-between">
            <span class="text-base-content/70">Secret</span>
            <span class="font-medium" data-testid="db-summary-passphrase">
              {dbWizardStore.barrierPassphrase !== "" ? "Set" : "Not set"}
            </span>
          </div>
        {/if}
        {#if dbWizardStore.sealMode === "shamir"}
          <div class="flex items-center justify-between">
            <span class="text-base-content/70">Shamir Scheme</span>
            <span class="font-medium">
              {dbWizardStore.sealConfig.sharesRequired}-of-{dbWizardStore
                .sealConfig.sharesTotal}
            </span>
          </div>
        {/if}
      </div>
    </div>
  </div>

  <div class="flex w-full max-w-2xl justify-between">
    <button class="btn btn-ghost" onclick={onBack}>Back</button>
    <button class="btn btn-primary" onclick={onNext}>Create Database</button>
  </div>
</div>
