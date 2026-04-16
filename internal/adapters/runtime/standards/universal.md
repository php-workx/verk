# Universal Review Standards

Apply these checks regardless of programming language. Findings from these rules are
typically P0–P1.

---

## Error Propagation at State Boundaries

Functions affecting persisted state, scope/safety decisions, or API return values must
propagate errors. Silent discard at these boundaries is a blocking finding.

| Pattern | Language | Verdict |
|---------|----------|---------|
| `_ = mutateState()` without comment | Go | FAIL |
| `catch (e) {}` empty body | JS/TS | FAIL |
| `except: pass` without comment | Python | FAIL |
| `_ = cleanup() // best-effort, logged above` | Go | OK |

**Specific anti-patterns to catch:**
- Functions that collect data for safety/scope checks returning empty results on failure
  — the check then trivially passes. Must return `(T, error)`.
- API/CLI functions returning success (nil/0) when the persisted run state shows
  failure/blocked. Return value must match persisted state.
- Deferred cleanup blocks using `_ =` on state-mutating calls with no logging.

---

## Safety Checks Must Fail Closed

Validation and scope-checking functions must not return success on empty/nil input.

- Does the scope check return nil (pass) when `owned_paths` is empty? → Should warn or error.
- Does a git/IO failure produce empty results that silently pass the check? → Must be an error.
- Does a wave-level check use the union of all scopes instead of per-item ownership?
  → Must validate each item against its own declared scope.

**Principle:** The default answer to a safety question is NO, not YES.

---

## Aggregate/Summary Fields Must Be Computed

Fields summarising multiple underlying items must be derived from current state, never
hardcoded or defaulted to the most common case.

- `CurrentPhase` for a run → compute from non-terminal ticket phases; don't default to Implement.
- `EffectiveThreshold` across tickets → use a policy rule (e.g. most restrictive); not alphabetical sort.
- If a field is named `Current*`, `Effective*`, or `Aggregate*` — verify it is computed.

---

## TTL/Interval Scheduling: Use Remaining Time for Resumed Resources

For scheduling renewals or timeouts on *existing* resources (resumed claims, partially-aged
leases), compute the interval from remaining time — not original duration.

| Resource state | Correct reference |
|----------------|------------------|
| Fresh | `expiresAt - createdAt` (full TTL) |
| Resumed/existing | `expiresAt - now` (remaining TTL) |

The *amount to extend by* still uses the full TTL. Only the *scheduling delay* uses remaining time.
When remaining time is below the renewal threshold, renew immediately.

---

## State Mutation Ordering

Never write to durable files (markdown, JSON on disk) before the atomic commit/transition
that formalises the state change.

- Does the code mutate a file, then commit? What happens if the commit fails?
- Correct order: compute new state in memory → commit atomically → update ancillary files.
- If pre-commit mutation is unavoidable: implement explicit rollback on commit failure.

---

## Input Validation Before Path Construction

Any ID or user-supplied value used in `filepath.Join` / `path.join` / `os.path.join`
must be validated first.

Validator must reject:
- Path separators (`/`, `\`)
- Dot-dot traversal (`..`)
- Absolute paths (starts with `/` or drive letter)
- Characters outside the allowed ID charset

Also verify the cleaned path stays within the expected base directory after joining.

| Language | Safe pattern |
|----------|-------------|
| Go | `cleaned := filepath.Clean(id); strings.HasPrefix(filepath.Join(base, cleaned), base)` |
| Python | `r = (base / id).resolve(); str(r).startswith(str(base.resolve()))` |
| Node | `r = path.resolve(base, id); r.startsWith(base)` |

---

## Round-Trip Idempotency

A load-save cycle must produce identical file content when no intentional change was made.

- Is the provenance of derived fields tracked? (e.g. title from heading vs. from frontmatter)
- Does the saver write derived fields back as authoritative data?
- Test: load a file, save unchanged, diff — output must be identical.

---

## Config/Defaults Completeness

Every field in a config struct that has a non-zero default must be explicitly initialised
in the normalisation function. Missing fields silently use Go/language zero values.

- `MaxRepairCycles = 0` means the first repair immediately exceeds the limit.
- `MaxAttempts = 0` means no attempts are allowed.
- After writing a normalisation function: enumerate every field — verify each has a default applied.

---

## Error Handling Anti-Patterns (Universal)

| Anti-Pattern | Why Bad | Instead |
|--------------|---------|---------|
| Silent suppression without comment | Hides bugs, makes debugging impossible | Log, propagate, or document |
| String-only errors | Not matchable/programmable | Use typed/structured errors |
| Catching too broadly | Masks unrelated failures | Catch the most specific type |
| Log AND re-raise at every layer | Duplicate log entries | Log at boundary, propagate elsewhere |
| Return success when persisted state shows failure | Caller gets wrong signal | Return error matching persisted state |

---

## Security: Injection Prevention

| Attack vector | Prevention |
|---------------|-----------|
| SQL | Parameterised queries only — never string interpolation |
| Command | Array-based exec — no shell, no `eval()` |
| Path | Validate before joining — see Input Validation above |
| JSON/YAML | Use proper serialisation libraries — never string interpolation |
