// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/**
 * Theme state store.
 *
 * Manages the active DaisyUI theme with localStorage persistence. The
 * theme value is applied to `document.documentElement.dataset.theme` so
 * that DaisyUI's CSS variables resolve correctly.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Supported theme values. */
export type Theme = 'light' | 'dark';

/** Shape of the reactive theme state. */
export interface ThemeState {
    /** Currently active DaisyUI theme. */
    theme: Theme;
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** localStorage key used to persist the theme preference. */
const STORAGE_KEY = 'qrdb-theme';

/** Default theme when no persisted preference exists. */
const DEFAULT_THEME: Theme = 'dark';

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

/** Reactive theme state. */
export const themeStore: ThemeState = $state({
    theme: DEFAULT_THEME,
});

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/**
 * Validates that a string is a supported theme value.
 */
function isValidTheme(value: string): value is Theme {
    return value === 'light' || value === 'dark';
}

/**
 * Applies the given theme to the document root element's `data-theme`
 * attribute. Safe to call during SSR (no-ops when `document` is unavailable).
 */
function applyTheme(theme: Theme): void {
    if (typeof document !== 'undefined') {
        document.documentElement.dataset['theme'] = theme;
    }
}

/**
 * Persists the given theme to localStorage. Safe to call during SSR
 * (no-ops when `localStorage` is unavailable).
 */
function persistTheme(theme: Theme): void {
    if (typeof localStorage !== 'undefined') {
        localStorage.setItem(STORAGE_KEY, theme);
    }
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/**
 * Toggles between the light and dark themes, persists the choice to
 * localStorage, and applies the change to the document root.
 */
export function toggleTheme(): void {
    const next: Theme = themeStore.theme === 'dark' ? 'light' : 'dark';
    themeStore.theme = next;
    persistTheme(next);
    applyTheme(next);
}

/**
 * Reads the persisted theme from localStorage (falling back to the default)
 * and applies it to the document. This should be called once during
 * application initialization.
 */
export function initTheme(): void {
    let theme: Theme = DEFAULT_THEME;

    if (typeof localStorage !== 'undefined') {
        const stored = localStorage.getItem(STORAGE_KEY);
        if (stored !== null && isValidTheme(stored)) {
            theme = stored;
        }
    }

    themeStore.theme = theme;
    applyTheme(theme);
}
