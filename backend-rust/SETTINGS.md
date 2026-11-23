# Phase 7 settings policy boundary

Active in opt-in Windows amd64 rustcore builds:
- windowsGameRule uses Rust rule formatting, including anchored regexp escaping
  for literal paths containing commas.
- normalizeGameBypass uses Rust Android package validation, Windows validity
  aggregation, stable ordered deduplication and exact errors.
- updateSettings calls the pure SOCKS port/platform predicate. Collision checks,
  settings mutation, persistence and reconnection stay Go-owned. The new tests
  never call updateSettings.

Go retains filepath.IsAbs/Clean/Ext, executable-name Unicode regexp, TrimSpace,
and ToLower dedupe keys to preserve platform behavior. Original Go functions
remain independent oracles. Default builds, old DLLs lacking settings symbols,
invalid ABI results and invalid UTF8 use Go fallback. ABI bounds: 8 MiB input,
16 MiB retried output. Empty normalization returns an allocated empty list,
not nil; semantic errors return nil and the original message.

TestRustSettingsParity compares 26 entries pairwise across both modes (1,352
lists), plus rule strings and port boundaries. Synthetic inputs include Unicode,
UNC/drive-relative paths, cleanup, commas, controls, invalid UTF8, Android invalid
segments and package case. Four Rust tests cover grammar/rule encoding, ordering,
port bounds and ABI errors. No real files, services or network are accessed.
