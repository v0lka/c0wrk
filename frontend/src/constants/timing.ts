// Centralized timing constants for debouncing and thresholds across hooks.
// Avoids magic numbers scattered in individual components (S-40).

/** Debounce for file tree search input (ms) */
export const DEBOUNCE_SEARCH_MS = 300

/** Debounce for window/panel resize handlers (ms) */
export const DEBOUNCE_RESIZE_MS = 150

/** Auto-scroll threshold — distance from bottom (px) to trigger auto-scroll */
export const AUTO_SCROLL_THRESHOLD_PX = 50

/** Debounce for vector index incremental re-indexing (ms) */
export const DEBOUNCE_INDEX_MS = 1000
