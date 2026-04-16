# Go-Specific Review Checklist

Auto-injected when the diff contains `.go` files.
Sourced from Baz Awesome Reviewers and AgentOps Go standards.

---

## Error Handling

- [ ] **Bare error return without context**: Is `return err` used instead of
  `return fmt.Errorf("what failed: %w", err)`? Bare returns produce opaque chains.
  <!-- argo-cd-wrap-errors-with-context, influxdb-wrap-errors-with-context -->

- [ ] **Unchecked error return**: Is an error return assigned to `_` or ignored?
  Even "unlikely" errors (Close, Flush, regex compile) can indicate real failures.
  Bad: `f.Close()`. Good: `if err := f.Close(); err != nil { log.Warn(...) }`.
  <!-- fiber-check-all-error-returns, cli-handle-all-errors-explicitly -->

- [ ] **Resource leak on error path**: When an error occurs after opening a resource
  (file, connection, response body), is it closed? Use `defer` immediately after
  successful open, before the next error check.
  <!-- terraform-resource-cleanup-on-errors, grafana-close-resources-with-errors -->

- [ ] **Close error silenced on write path**: For writable resources (file, gzip writer,
  DB transaction), is the `Close()` error propagated? A failed Close can mean data
  was not flushed. Bad: `defer w.Close()`. Good: named return + `defer func() { if cerr := w.Close(); err == nil { err = cerr } }()`.
  <!-- grafana-close-resources-with-errors -->

- [ ] **`%v` for errors**: Breaks error chain — use `%w`.

- [ ] **`err == target`**: Misses wrapped errors — use `errors.Is(err, target)`.

---

## Nil & Panic Safety

- [ ] **Nil/empty slice confusion in OS interfaces**: For `exec.Cmd.Env`, `nil` means
  inherit the parent environment; `[]string{}` means an empty environment (no PATH,
  no HOME). Never replace a nil return with `[]string{}` without verifying semantics.
  <!-- sonic-empty-vs-nil-distinction -->

- [ ] **Nil/empty slice confusion in serialisation**: JSON encodes `nil` as `null`
  and `[]T{}` as `[]`. Don't treat as equivalent when it affects API contracts.

- [ ] **Nil dereference after type assertion**: A successful `.(*T)` assertion can still
  yield a nil pointer. After `if v, ok := x.(*Foo); ok { ... }`, add `&& v != nil`.
  <!-- volcano-add-explicit-nil-checks -->

- [ ] **Missing nil check on pointer parameter**: Exported functions and interface
  implementations should guard against nil pointer arguments.
  <!-- influxdb-prevent-nil-dereferences, grafana-explicit-null-validation -->

- [ ] **Panic in production error path**: `panic()` in library code crashes the entire
  process. Return errors instead; reserve panic for invariant violations.
  <!-- prometheus-avoid-panics-gracefully -->

---

## Concurrency

- [ ] **Missing defer on mutex unlock**: Every `mu.Lock()` must be followed immediately
  by `defer mu.Unlock()`. A panic or early return between them causes a deadlock.
  Bad: `mu.Lock(); doWork(); mu.Unlock()`.
  <!-- vitess-prevent-concurrent-access-races, influxdb-lock-with-defer-unlock -->

- [ ] **Shared map/slice without synchronisation**: Maps are not safe for concurrent
  use. Wrap with `sync.Mutex` or use `sync.Map`.
  <!-- waveterm-protect-shared-state, grafana-safe-concurrent-programming -->

- [ ] **Goroutine leak via `context.Background()`**: Spawned goroutines should inherit
  the parent context. `context.Background()` goroutines cannot be cancelled.
  Bad: `go doWork(context.Background())`. Good: `go doWork(ctx)`.
  <!-- grafana-safe-concurrent-programming, istio-prevent-race-conditions -->

- [ ] **`time.Sleep` ignoring cancellation**: Use
  `select { case <-time.After(d): case <-ctx.Done(): return }` instead.
  <!-- istio-prevent-race-conditions -->

- [ ] **`t.Fatalf` from non-test goroutine**: Panics in Go 1.22+. Use a channel to
  communicate results back to the test goroutine instead.

- [ ] **Returning internal map/slice without copy**: Return `slices.Clone(s.items)` or
  `maps.Clone(s.data)` to prevent callers from mutating backing data.
  <!-- waveterm-protect-shared-state, vitess-prevent-concurrent-access-races -->

---

## Config & Defaults

- [ ] **Config normalisation completeness**: When normalising a config struct, every
  field with a non-zero default must be explicitly populated. Zero values are not
  safe defaults for limits and counts (`MaxRepairCycles = 0` → first repair fails).
  Grep all `policy.DefaultConfig()` fields; verify each appears in the normaliser.

- [ ] **Wire input validation**: Enum-like fields parsed from JSON/YAML must be
  validated against an allowlist before use. Don't trust the wire spelling (e.g.
  `needs-more-context` vs `needs_more_context` after normalisation).

- [ ] **Struct contract completeness**: When adding a field, grep all `StructName{`
  literals — every constructor must populate the new field. Partial population
  creates an inconsistent contract for consumers.

---

## Security

- [ ] **Unsanitised path in file operation**: User input in `filepath.Join` without
  validation enables path traversal (`../../../etc/passwd`). Validate before joining:
  reject `..`, `/`, `\`, absolute paths.
  <!-- argo-cd-validate-untrusted-inputs -->

- [ ] **Secrets in error messages or logs**: Log the event, not the value. Never include
  raw user input, environment variables, or credentials in error strings.
  <!-- kubernetes-prevent-information-disclosure -->

- [ ] **SQL built with string concatenation**: Always use parameterised queries.
  <!-- vitess-use-parameterized-queries -->

---

## Testing

- [ ] **Test names must match what is tested**: A test named `_OnlyOneWins` or
  `_Exclusive` must actually verify mutual exclusion — not just that at least one
  goroutine succeeded. Use a shared counter or channel to verify at-most-one holds
  the resource at any instant.

- [ ] **Exact assertion rule**: Always assert `got == expected`, never `got != wrong`.
  `!= wrong` silently passes when the result drifts to a different wrong value.

- [ ] **No zero-assertion tests**: Every test must assert behavioural correctness, not
  just "it didn't panic".

- [ ] **Table-driven tests for multi-case functions**: Prefer
  `[]struct{ name, input, want }` + `t.Run()` over repeated similar test functions.

---

## Memory & Performance

- [ ] **Slice append aliasing**: `append(subSlice, elem)` overwrites parent data when
  spare capacity exists. Use `slices.Clip(s)` before appending from a sub-slice.
  <!-- opentofu-prevent-backing-array-surprises -->

- [ ] **Allocation in hot path**: Pre-allocate with `make([]T, 0, n)` or reuse via
  `sync.Pool`. Avoid `make([]T, len(input))` on every call to a hot function.
  <!-- prometheus-minimize-memory-allocations, ollama-reuse-buffers-strategically -->

---

## Networking

- [ ] **HTTP call without context/timeout**: `http.Get(url)` can hang forever.
  Use `http.NewRequestWithContext(ctx, "GET", url, nil)`.
  <!-- waveterm-use-network-timeouts, temporal-context-aware-network-calls -->

---

## Observability

- [ ] **Trace span not closed on all return paths**: Always `defer span.End()`
  immediately after `tracer.Start()`. Unclosed spans leak memory and distort traces.
  <!-- opentofu-proper-span-lifecycle -->
