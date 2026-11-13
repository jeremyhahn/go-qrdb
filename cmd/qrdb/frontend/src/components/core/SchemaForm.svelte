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
  import { Plus, X } from "lucide-svelte";

  interface Props {
    schema: Record<string, string>;
    value: Record<string, unknown>;
    onchange: (fields: Record<string, unknown>) => void;
    disabled?: boolean;
  }

  let { schema, value, onchange, disabled = false }: Props = $props();

  let jsonErrors: Record<string, string> = $state({});
  let newFieldName: string = $state("");
  let newFieldType: string = $state("VARCHAR");

  // Tracks dynamically added fields (name → SQL type) that are not in the
  // original schema prop. Merged with schema for rendering.
  let dynamicSchema: Record<string, string> = $state({});

  // Use the parent's value prop as the single source of truth for field
  // data. All edits flow through onchange to update the parent, which
  // passes the updated value back via the prop.
  const fields = $derived(value);

  // Merge the original schema with dynamically added fields, and also
  // include any fields present in the value that are not in either schema.
  const mergedSchema = $derived.by((): Record<string, string> => {
    const merged: Record<string, string> = {
      ...schema,
      ...dynamicSchema,
    };
    // Add fields from value that aren't in schema yet (e.g. from JSON edit).
    for (const key of Object.keys(value)) {
      if (merged[key] === undefined) {
        merged[key] = inferSqlType(value[key]);
      }
    }
    return merged;
  });

  const sortedFields = $derived(
    Object.entries(mergedSchema).sort(([a], [b]) => a.localeCompare(b))
  );

  const addFieldTypes = ["VARCHAR", "DOUBLE", "BIGINT", "BOOLEAN", "JSON"] as const;

  function handleFieldChange(name: string, sqlType: string, rawValue: unknown): void {
    const parsed = parseValue(sqlType, rawValue);
    onchange({ ...value, [name]: parsed });
  }

  function removeField(name: string): void {
    const updated = { ...value };
    delete updated[name];

    // Also remove from dynamic schema if it was added dynamically.
    if (dynamicSchema[name] !== undefined) {
      const updatedDynamic = { ...dynamicSchema };
      delete updatedDynamic[name];
      dynamicSchema = updatedDynamic;
    }

    onchange(updated);
  }

  function parseValue(sqlType: string, raw: unknown): unknown {
    const upper = sqlType.toUpperCase();
    switch (upper) {
      case "DOUBLE": {
        const n = parseFloat(String(raw));
        return Number.isNaN(n) ? 0.0 : n;
      }
      case "BIGINT": {
        // Keep as string to avoid JavaScript precision loss for int64 values.
        const s = String(raw).trim();
        return s === "" ? "0" : s.replace(/[^0-9-]/g, "") || "0";
      }
      case "BOOLEAN":
        return Boolean(raw);
      case "JSON": {
        try {
          return JSON.parse(String(raw));
        } catch {
          return raw;
        }
      }
      default:
        return String(raw);
    }
  }

  function getDisplayValue(name: string, sqlType: string): string | number | boolean {
    const val = fields[name];
    if (val === undefined || val === null) return "";
    const upper = sqlType.toUpperCase();
    if (upper === "JSON") {
      return typeof val === "object" ? JSON.stringify(val, null, 2) : String(val);
    }
    if (upper === "BOOLEAN") return Boolean(val);
    return val as string | number;
  }

  function validateJsonField(name: string, raw: string): void {
    try {
      JSON.parse(raw);
      delete jsonErrors[name];
      jsonErrors = { ...jsonErrors };
    } catch {
      jsonErrors = { ...jsonErrors, [name]: "Invalid JSON syntax." };
    }
  }

  function addField(): void {
    const trimmed = newFieldName.trim();
    if (trimmed === "") return;
    if (mergedSchema[trimmed] !== undefined) return;
    dynamicSchema = { ...dynamicSchema, [trimmed]: newFieldType };
    const defaultValue = getDefaultForType(newFieldType);
    onchange({ ...value, [trimmed]: defaultValue });
    newFieldName = "";
  }

  function getDefaultForType(sqlType: string): unknown {
    switch (sqlType.toUpperCase()) {
      case "DOUBLE":
        return 0.0;
      case "BIGINT":
        return 0;
      case "BOOLEAN":
        return false;
      case "JSON":
        return {};
      default:
        return "";
    }
  }

  function inferSqlType(val: unknown): string {
    if (typeof val === "boolean") return "BOOLEAN";
    if (typeof val === "number") {
      return Number.isInteger(val) ? "BIGINT" : "DOUBLE";
    }
    if (typeof val === "object" && val !== null) return "JSON";
    return "VARCHAR";
  }
</script>

<div class="flex flex-col gap-5">
  {#each sortedFields as [name, sqlType] (name)}
    {@const upper = sqlType.toUpperCase()}
    <div class="form-control w-full">
      <div class="flex items-center justify-between">
        <label class="label" for="field-{name}">
          <span class="label-text font-medium">{name}</span>
          <span class="label-text-alt text-base-content/40 ml-2">{sqlType}</span>
        </label>
        {#if !disabled}
          <button
            class="btn btn-ghost btn-xs text-base-content/40 hover:text-error"
            onclick={() => removeField(name)}
            aria-label="Remove field {name}"
          >
            <X class="h-3.5 w-3.5" />
          </button>
        {/if}
      </div>

      {#if upper === "BOOLEAN"}
        <label class="flex cursor-pointer items-center gap-3 py-1">
          <input
            id="field-{name}"
            type="checkbox"
            class="toggle toggle-primary"
            checked={Boolean(getDisplayValue(name, sqlType))}
            {disabled}
            onchange={(e) => handleFieldChange(name, sqlType, e.currentTarget.checked)}
          />
          <span class="label-text text-base-content/60">
            {getDisplayValue(name, sqlType) ? "true" : "false"}
          </span>
        </label>
      {:else if upper === "JSON"}
        <textarea
          id="field-{name}"
          class="textarea textarea-bordered w-full font-mono text-sm"
          rows="4"
          {disabled}
          value={String(getDisplayValue(name, sqlType))}
          oninput={(e) => {
            const raw = e.currentTarget.value;
            validateJsonField(name, raw);
            handleFieldChange(name, sqlType, raw);
          }}
        ></textarea>
        {#if jsonErrors[name]}
          <div class="label">
            <span class="label-text-alt text-error">{jsonErrors[name]}</span>
          </div>
        {/if}
      {:else if upper === "DOUBLE"}
        <input
          id="field-{name}"
          type="number"
          step="any"
          class="input input-bordered w-full"
          {disabled}
          value={getDisplayValue(name, sqlType)}
          oninput={(e) => handleFieldChange(name, sqlType, e.currentTarget.value)}
        />
      {:else if upper === "BIGINT"}
        <input
          id="field-{name}"
          type="number"
          step="1"
          class="input input-bordered w-full"
          {disabled}
          value={getDisplayValue(name, sqlType)}
          oninput={(e) => handleFieldChange(name, sqlType, e.currentTarget.value)}
        />
      {:else}
        <input
          id="field-{name}"
          type="text"
          class="input input-bordered w-full"
          {disabled}
          value={String(getDisplayValue(name, sqlType))}
          oninput={(e) => handleFieldChange(name, sqlType, e.currentTarget.value)}
        />
      {/if}
    </div>
  {/each}

  <div class="border-base-content/10 mt-4 border-t pt-4">
    <div class="flex items-end gap-3">
      <div class="form-control flex-1">
        <label class="label" for="new-field-name">
          <span class="label-text">Field name</span>
        </label>
        <input
          id="new-field-name"
          type="text"
          class="input input-bordered w-full"
          placeholder="New field name"
          {disabled}
          bind:value={newFieldName}
          onkeydown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              addField();
            }
          }}
        />
      </div>

      <div class="form-control">
        <label class="label" for="new-field-type">
          <span class="label-text">Type</span>
        </label>
        <select
          id="new-field-type"
          class="select select-bordered"
          {disabled}
          bind:value={newFieldType}
        >
          {#each addFieldTypes as t (t)}
            <option value={t}>{t}</option>
          {/each}
        </select>
      </div>

      <button
        class="btn btn-primary"
        {disabled}
        onclick={addField}
        aria-label="Add field"
      >
        <Plus class="h-4 w-4" />
        Add
      </button>
    </div>
  </div>
</div>
