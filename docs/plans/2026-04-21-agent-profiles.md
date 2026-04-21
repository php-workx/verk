# Agent Profiles

## Metadata

- Date: 2026-04-21
- Status: planned
- Scope: Role-based worker profiles — taxonomy, detection, frontmatter field, pre-run validation, rationalization injection, and prompt placement.
- Out of scope: Reviewer role specialisation (separate concern); compiled-constraint promotion (P7 in impl-verify-improvements); wave/epic-level profiles.
- Related: [Rationalizations.md](Rationalizations.md) (rationalization catalog), [impl-verify-improvements](2026-04-19-impl-verify-improvements.md) P3 (anti-rationalization injection), [ticket-quality-gate](2026-04-21-ticket-quality-gate.md) (pre-run validation home).

---

## Problem

verk workers are generic agents. Every worker receives the same system prompt regardless of whether the ticket is hardening a credential store, wiring a public CLI flag, building a UI component, or fixing internal engine logic. The worker's vigilance surface — what it notices as a problem, what it considers "done", what rationalizations it's likely to use to cut corners — is identical across all of these.

The failure mode this creates: a worker implementing a CLI command optimises for making the implementation compile and tests pass. It does not naturally ask "does `--flag` appear in `--help`?", "is the exit code contract honoured?", or "is this flag consistent with the existing surface?" Those questions belong to a *contract engineer's* vigilance, not a generic implementer's.

Role-based profiles solve this by injecting a targeted vigilance framing at prompt construction time — not as additional rules, but as named rationalizations that preempt the specific shortcuts the worker would otherwise take.

---

## Design Principles

1. **Profiles are vigilance profiles, not scope restrictions.** A `security-engineer` profile does not prevent the worker from touching non-security files. It tells the worker what class of problems to be *especially alert to*.
2. **Detection is project-agnostic.** Signals must work across Go, TypeScript, Python, Rust, and any future language. Directory conventions (`internal/`, `cmd/`) are not signals. File extensions and ticket content are.
3. **Four roles, not eight.** Consolidation over completeness. Roles that share a vigilance profile are merged. Techniques (incremental implementation, TDD) are universal injections, not roles.
4. **Pre-run selection, not worker self-selection.** The profile is chosen during pre-run ticket verification before the worker starts. The worker receives its profile as a given, not something it reasons about.
5. **Explicit overrides the detected.** A ticket author can set `profile:` in frontmatter. The pre-run gate validates the value is known; it does not override an explicit declaration.
6. **Prompt placement matters as much as content.** Rationalization tables are effective only when placed where the worker reads them before forming its implementation plan — not buried after standards.

---

## The Four Profiles

### `security-engineer`

Work type: hardening, authentication, secrets management, input validation, sandboxing, attack surface reduction.

Vigilance: credential leakage, attack surface, input sanitisation, env scrubbing, secret redaction, privilege boundaries.

Detection priority: **1 — wins all conflicts.**

Detection signals (any match):
- Ticket tags contain `security`, `auth`, `hardening`, `CVE`, `pentest`
- Ticket body or acceptance criteria contain: `token`, `credential`, `redact`, `signing`, `sandbox`, `injection`, `privilege`, `secret`, `auth`
- File extensions: `.pem`, `.key`, `.crt` in `owned_paths`

Rationalization groups injected:
1. Security and Hardening (primary)
2. Test-Driven Development (universal)
3. API and Interface Design (subset: input contracts, trust boundaries)

---

### `contract-engineer`

Work type: public surfaces — CLI commands and flags, REST/gRPC API endpoints, SDK exports, IPC protocols. Any work where the declared interface is the deliverable.

Vigilance: every declared command/flag/endpoint/export works exactly as specified; exit codes are correct; `--help` is accurate; backward compatibility is preserved; undocumented behaviour is treated as a commitment (Hyrum's Law).

Detection priority: **2.**

Detection signals (any match):
- Ticket acceptance criteria contain: `exit code`, `exit_code`, `--flag`, `--help`, `subcommand`, `public API`, `backward compat`, `export`, `endpoint`, `versioning`, `pagination`, `contract`
- Ticket body contains: `CLI`, `argparse`, `cobra`, `public surface`, `wire format`, `RPC`, `gRPC`, `REST`
- Ticket tags contain: `cli`, `api`, `sdk`, `interface`, `contract`

Rationalization groups injected:
1. API and Interface Design (primary)
2. Test-Driven Development (universal — Prove-It on every declared command)
3. Source-Driven Development (verify against spec/plan, not from memory)

---

### `frontend-engineer`

Work type: UI components, browser runtime, accessibility, CSS, responsive design, visual correctness, DevTools verification.

Vigilance: accessibility (ARIA, keyboard nav, contrast), DOM correctness, CSS layout, browser runtime behaviour diverging from unit test assumptions, real render vs. mental model.

Detection priority: **3.**

Detection signals (any match):
- File extensions in `owned_paths`: `.tsx`, `.jsx`, `.vue`, `.svelte`, `.css`, `.scss`, `.html`
- Ticket body or acceptance criteria contain: `component`, `render`, `DOM`, `accessibility`, `responsive`, `CSS`, `layout`, `browser`, `viewport`
- Ticket tags contain: `frontend`, `ui`, `browser`, `a11y`

Rationalization groups injected:
1. Frontend / UI Engineering (primary)
2. Browser Testing with DevTools (runtime verification)
3. Test-Driven Development (universal)

---

### `backend-engineer`

Work type: internal logic, data access, service internals, engine behaviour, build/platform tooling, migration work, documentation. **Default fallback when no other profile matches.**

Vigilance: error handling, state correctness, data integrity, N+1 queries, concurrency, code simplicity, scope discipline.

Detection priority: **4 — fallback.**

Detection signals: anything not matched by priorities 1–3.

Rationalization groups injected:
1. Debugging and Error Recovery (primary — reproduce before fixing)
2. Code Simplification (scope discipline, smallest change)
3. Test-Driven Development (universal)

---

## Universal Injections (All Profiles)

These are not profile-specific. They are injected into every worker prompt regardless of profile:

- **Test-Driven Development** — Prove-It pattern: write a failing test before implementing, show BEFORE state, then AFTER state.
- **Incremental Implementation** — commit each slice, test each slice, no "I'll test it all at the end."

Because TDD appears in every profile's injected groups above, these are already accounted for in the token budget. They are listed here to make clear they are unconditional.

---

## Frontmatter Field

New optional field added to the ticket schema:

```markdown
---
id: abc-1234
status: open
profile: contract-engineer
...
---
```

**Valid values:** `security-engineer`, `contract-engineer`, `frontend-engineer`, `backend-engineer`

**When absent:** pre-run ticket verification auto-detects and writes the field before the worker starts.

**When present:** pre-run gate validates the value is a known profile. Invalid values are a pre-run lint error — the run does not start until resolved (auto-repaired to the detected value, or operator-corrected).

**Schema change:** `profile` is added to the known frontmatter keys in `tkmd/store.go` alongside `runtime`, `model`, and `review_threshold`. Unknown profile values surface as a `ValidateTicketSchedulingFields` error.

---

## Detection Algorithm

Run at pre-run ticket verification time. Signals are evaluated in priority order; first match wins.

```go
func DetectProfile(ticket Ticket) Profile {
    // Priority 1: security
    if matchesSecurity(ticket) {
        return ProfileSecurity
    }
    // Priority 2: contract
    if matchesContract(ticket) {
        return ProfileContract
    }
    // Priority 3: frontend
    if matchesFrontend(ticket) {
        return ProfileFrontend
    }
    // Priority 4: fallback
    return ProfileBackend
}
```

Each matcher checks:
1. Tags (exact set intersection)
2. Acceptance criteria text (keyword scan)
3. Body text (keyword scan)
4. `owned_paths` file extensions

Keyword matching is case-insensitive, whole-word where practical. The matcher does **not** use LLM inference — it is deterministic and fast, suitable for a pre-run lint gate with no token spend.

### Conflict Resolution

Priority ordering is strict. A ticket touching both CLI flags and auth handling is `security-engineer` — security wins. The contract-surface concern is injected as part of the security profile's API/Interface rationalization group anyway.

There is no primary+secondary profile. Two profiles dilute both vigilance surfaces. Strict priority plus thoughtfully chosen rationalization groups covers the cross-cutting cases adequately.

---

## Prompt Placement

Token budget is not the concern — placement is. Rationalization tables placed after the full standards block (4,100+ tokens) and after code context are effectively invisible: by the time the worker reads them, it has already formed its implementation plan.

**Target placement in worker prompt:**

```text
[1] Role framing sentence           ← new, ~30 tokens
[3] Ticket content                  ← existing
[2] Rationalization tables          ← new, ~600 tokens
[4] WorkerSystemPrompt() rules      ← existing, move here
[5] Standards (universal + lang)    ← existing
[6] Code context (owned_paths)      ← existing
```

Rationale: the worker reads the prompt top-to-bottom before taking any action. Rationalizations placed at position [2] — after ticket content and before standards — prime the worker's vigilance once the requirements are understood, but before it dives into implementation mechanics. This mirrors how the agent-skills pattern works: the counter-argument is in working memory when the temptation arises.

The role framing sentence [1] is deliberately short:

> "You are acting as a **contract-engineer**. Your primary vigilance is public surface faithfulness: every declared command, flag, endpoint, and export must work exactly as specified."

One sentence. The rationalization tables do the elaboration.

**Open question:** whether to place rationalizations before or after the ticket content. Arguments:

- **Before ticket:** rationalizations prime the vigilance frame before the worker reads requirements. Risk: worker might anchor on rationalizations before understanding what it's building.
- **After ticket, before standards:** worker understands the task first, then receives the vigilance frame before diving into language standards and code. Likely the better position — requirements first, vigilance second, mechanics third.

Recommendation: **after ticket content, before standards.** Finalise based on empirical runs once implemented.

---

## Token Budget

Full worker prompt with profiles added (worst case: Go ticket):

| Component | Tokens | Notes |
|---|---|---|
| Role framing sentence | ~30 | New |
| Rationalization tables (3 groups) | ~600 | New — cache candidate |
| `WorkerSystemPrompt()` | ~212 | Existing |
| `BuildWorkerPrompt()` base | ~100 | Existing |
| Ticket content | ~500 | Existing — varies |
| `universal.md` standards | ~1,397 | Existing — cache candidate |
| `go.md` standards | ~1,792 | Existing — cache candidate |
| `cross_platform.md` standards | ~926 | Existing — cache candidate |
| `owned_paths` file content | ~2,000–8,000 | Existing — not cached |
| **Grand total** | **~7,500–13,500** | |

Rationalizations are ~6–11% of the full prompt. The system prompt + standards + rationalizations are static per profile per language — all are prompt cache candidates. Rationalization token cost is effectively zero after the first call per session.

---

## Implementation Tasks

### Task 1: Schema — Add `profile` to ticket frontmatter

**Files:**
- `internal/adapters/ticketstore/tkmd/types.go`
- `internal/adapters/ticketstore/tkmd/store.go`
- `internal/adapters/ticketstore/tkmd/store_test.go`

Add `Profile string` to `Ticket` struct. Add `profile` to the known frontmatter key set in `assignField` and `encodeFrontMatter`. Add `ProfileBackend`, `ProfileContract`, `ProfileFrontend`, `ProfileSecurity` constants. Add `ValidateProfile(p string) error` returning an error for unknown values.

Write failing tests first:
- `profile: contract-engineer` in frontmatter → `ticket.Profile == "contract-engineer"`
- Unknown value `profile: wizard` → `ValidateProfile` returns error
- Missing `profile` field → `ticket.Profile == ""`

---

### Task 2: Detection — `DetectProfile` function

**Files:**
- `internal/adapters/ticketstore/tkmd/profile.go` (new)
- `internal/adapters/ticketstore/tkmd/profile_test.go` (new)

Implement `DetectProfile(ticket Ticket) Profile` with the priority-ordered matcher. Detection returns one of the enum-like constants (`ProfileSecurity`, `ProfileContract`, `ProfileFrontend`, `ProfileBackend`) and is a pure function — no I/O, no LLM, no file reads beyond what the ticket struct already contains.

Write failing tests first:
- Ticket with tag `security` → `security-engineer`
- Ticket body containing `exit code` and `--flag` → `contract-engineer`
- Ticket with `.tsx` in `owned_paths` → `frontend-engineer`
- Ticket with no matching signals → `backend-engineer`
- Security signal + contract signal in same ticket → `security-engineer` (priority wins)

---

### Task 3: Pre-run gate — detect and write profile

**Files:**
- `internal/engine/intake.go` (or ticket quality gate, whichever owns pre-run validation)
- Corresponding test file

At pre-run validation time: if `ticket.Profile == ""`, call `DetectProfile`, write the detected value back to the ticket file via `SaveTicket`. If `ticket.Profile != ""`, call `ValidateProfile` — unknown value is a lint error with `reason: unknown_profile`.

Write failing tests first:
- Ticket without profile → after intake, file has `profile:` written with detected value
- Ticket with valid explicit profile → unchanged
- Ticket with invalid profile value → intake returns error with reason `unknown_profile`

---

### Task 4: Prompt construction — profile framing + rationalization injection

**Files:**
- `internal/adapters/runtime/prompt.go`
- `internal/adapters/runtime/profiles/` (new directory with one `.md` per profile)
- `internal/adapters/runtime/standards.go` (extend `BuildWorkerPrompt` or add `BuildProfilePrompt`)

Embed rationalization tables per profile as `.md` files alongside the existing `standards/*.md` pattern. Add `BuildProfilePrompt(profile Profile) string` returning the role framing sentence + rationalization tables for that profile. Return empty string for unknown profiles (graceful degradation — worker still runs without profile framing).

Modify `BuildWorkerPrompt` to accept a `Profile` and insert the profile block after ticket content and before the standards section. If rationalizations are rendered separately from the profile block, keep ticket content first, then rationalizations, then the profile block, then standards; do not prepend profile material before ticket content.

Write failing tests first:
- `BuildProfilePrompt("security-engineer")` contains "security" and "credential"
- `BuildProfilePrompt("contract-engineer")` contains "exit code" and "public surface"
- `BuildProfilePrompt("unknown-role")` returns `""`
- `BuildWorkerPrompt` with profile → ticket content appears before rationalizations/profile block, and the profile block appears before standards in output

---

### Task 5: `verk status` — surface active profile

**Files:**
- `internal/engine/status.go` (or wherever `StatusTicket` is populated)
- TUI/plain output

Add `Profile string` to `StatusTicket`. Surface it in `verk status` output alongside `runtime`. Operators need to see which profile was selected before or during a run to diagnose unexpected worker behaviour.

---

## Validation

Before closing this epic:

- [ ] `profile:` field round-trips through `LoadTicket` / `SaveTicket` without data loss
- [ ] `DetectProfile` returns correct profile for each signal type with no LLM calls
- [ ] Pre-run gate writes detected profile to ticket file before worker dispatch
- [ ] Invalid profile value blocks run with clear error message
- [ ] Worker prompt with `contract-engineer` profile contains rationalization tables specific to that profile
- [ ] Worker prompt profile block appears before standards in the assembled prompt
- [ ] `verk status` shows the active profile for each ticket
- [ ] All existing tests pass — no regressions to ticket parsing, worker dispatch, or review flow

---

## Relation to Other Plans

| Plan | Relation |
|---|---|
| [Rationalizations.md](Rationalizations.md) | Source catalog for rationalization groups. This plan selects which groups each profile injects. |
| [impl-verify-improvements P3](2026-04-19-impl-verify-improvements.md) | P3 is "anti-rationalization in worker prompts." This plan is P3's full implementation spec. |
| [ticket-quality-gate](2026-04-21-ticket-quality-gate.md) | Pre-run profile detection and validation (Task 3) lives in the ticket quality gate pipeline. |
| `tkmd/store.go` | Schema owner. `profile` field follows the same frontmatter pattern as `runtime` and `review_threshold`. |
