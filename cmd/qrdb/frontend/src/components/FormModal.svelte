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
  import type { Snippet } from "svelte";

  let {
    open = false,
    title = "Create",
    submitLabel = "Create",
    loading = false,
    onsubmit,
    oncancel,
    body,
  }: {
    open?: boolean;
    title?: string;
    submitLabel?: string;
    loading?: boolean;
    onsubmit?: () => void;
    oncancel?: () => void;
    body?: Snippet;
  } = $props();
</script>

{#if open}
<dialog class="modal modal-open">
  <div class="modal-box border border-base-content/6">
    <h3 class="text-base font-semibold">{title}</h3>
    <div class="py-6">
      {#if body}
        {@render body()}
      {/if}
    </div>
    <div class="modal-action">
      <button
        type="button"
        class="btn btn-ghost"
        onclick={oncancel}
        disabled={loading}
      >
        Cancel
      </button>
      <button
        type="button"
        class="btn btn-primary"
        onclick={onsubmit}
        disabled={loading}
      >
        {#if loading}
          <span class="loading loading-spinner loading-sm"></span>
        {/if}
        {submitLabel}
      </button>
    </div>
  </div>
  <form method="dialog" class="modal-backdrop">
    <button onclick={oncancel}>close</button>
  </form>
</dialog>
{/if}
