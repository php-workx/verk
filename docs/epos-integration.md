# verk → epos Migration: Implementation Plan & Handoff

**Status:** revised after pre-mortem. Ready to execute once epos `v0.2.0` is published with the required pre-flight commits.
**Scope:** Replace `verk/internal/adapters/ticketstore/tkmd` (3107 LOC, 28 call sites) with a new `verk/internal/adapters/ticketstore/epos` package backed by the `github.com/php-workx/epos` library.
**Source ADR:** `fabrikk-kb/decisions/0004-shared-ticket-system-architecture.md` (Phase 5)
**Source plan:** `$EPOS_ROOT/IMPLEMENTATION_PLAN.md` Epic E6.

---

## Context

epos `v0.2.0` will be published with the runtime, store, graph, and safepath changes listed below. verk must depend on `github.com/php-workx/epos@v0.2.0`; do not pin `v0.1.0` for this migration because it does not contain `eposruntime.WithLeaseID`, `store.ActiveClaimSet`, or upstream live-sidecar containment. fabrikk integration is paused (`go mod tidy` strips epos because no fabrikk file imports it yet). verk is the next consumer. verk currently bundles a private 3107-LOC `tkmd` package that implements its own Markdown frontmatter codec + claim/lease state machine; this duplicates ~80% of what epos already exposes via `ticket/markdown`, `ticket/store`, `ticket/runtime`, and `ticket/graph`.

**Goal:** verk reads/writes/claims tickets through epos so fabrikk and verk share one canonical schema and one claim sidecar format. verk-specific features (per-run durable claim archive, lease-fence semantics, OwnedPath validation) stay inside verk.

**Approved approach:**
1. Rename `tkmd` → `epos` package (verk-internal). Update all 28 call sites + the public type re-export at `pkg/verk/types.go:65`.
2. Keep verk's durable claim archive (`.verk/runs/<runID>/claims/claim-<ticketID>.json`) inside verk. Live sidecar (`.tickets/.claims/<id>.json`) is delegated to `eposruntime`.
3. Adapter normalizes epos extended-status values to verk's 5-state set on read; preserves the original `extended_status` for round-trip.
4. Adapter preserves tkmd child-discovery and body/path-write semantics. epos supplies the parser/runtime primitives; verk still owns compatibility behavior that is part of verk's product surface.

**Why this matters:** without verk integration, epos remains an unproven library; fabrikk won't trust the shared schema until a second consumer lands.

### Pre-flight required in epos v0.2.0

These commits must be included in the published `v0.2.0` tag before Phase 1 starts. Phases 4 and 5 below depend on them.

| Commit | What changed | Effect on this plan |
|---|---|---|
| `65248f2 feat(runtime): accept caller-supplied lease ID via WithLeaseID` | `Claim`, `Renew`, `ReclaimExpired` accept variadic `ClaimOption`; `WithLeaseID(string)` overrides the runtime-generated `<ticketID>-<nanos>` value. Validation rejects whitespace, path separators, null bytes. | Phase 5 drops the "ignore epos's lease ID, trust durable archive" workaround. The adapter preserves caller-supplied verk fence values, generates them only when absent, and passes the chosen value through directly. |
| `56a310e feat(runtime): symlink-aware sidecar path containment` | New `ticket/internal/safepath` package with `AssertUnderBase`/`AssertUnderIntendedBase`. `ReadRuntimeState`, `WriteRuntimeState`, and `withExclusiveLock` call `assertSidecarContained` before any I/O. | Live sidecar containment is free for verk. Verk's adapter only needs its own helpers for the durable-archive paths under `.verk/runs/<runID>/claims/`, not for `.tickets/.claims/...`. The internal package cannot be re-used from outside the epos module, so verk keeps its own copy. |
| `f9ecd4f feat(graph,store,cli): sidecar-aware epos ready` | `graph.ReadyFilterUnclaimed(all, isClaimed)`, `store.ActiveClaimSet()`, `store.ListReady()`. The `epos ready` CLI now hides actively claimed tickets unless `--include-claimed`. | Phase 4 reuses `store.ActiveClaimSet` for live-claim discovery. The adapter still performs tkmd-compatible child and ready filtering itself because epos graph filtering does not preserve verk's `status: ready` and epic-deps child semantics. |

**Version gate:** before editing verk, run `go list -m -versions github.com/php-workx/epos` and confirm `v0.2.0` is available. Then run `go get github.com/php-workx/epos@v0.2.0`. If `v0.2.0` is not published, stop; do not use a temporary `replace` in the implementation branch unless the user explicitly asks for a local-only spike.

---

## Naming Note (Avoid Import Collision)

The external library is `github.com/php-workx/epos`. It has subpackages:
- `github.com/php-workx/epos/ticket` (canonical types)
- `github.com/php-workx/epos/ticket/markdown`
- `github.com/php-workx/epos/ticket/store`
- `github.com/php-workx/epos/ticket/runtime`
- `github.com/php-workx/epos/ticket/graph`

The new verk-internal package will be at `verk/internal/adapters/ticketstore/epos` with package name `epos`. Inside its files, alias the external imports to avoid collision:

```go
import (
    eposticket "github.com/php-workx/epos/ticket"
    eposmarkdown "github.com/php-workx/epos/ticket/markdown"
    eposstore "github.com/php-workx/epos/ticket/store"
    eposruntime "github.com/php-workx/epos/ticket/runtime"
    eposgraph "github.com/php-workx/epos/ticket/graph"
)
```

Call sites outside the new package use `import "verk/internal/adapters/ticketstore/epos"` and refer to `epos.Ticket`, `epos.LoadTicket`, etc. — same shape as today's `tkmd.Ticket` / `tkmd.LoadTicket`.

---

## Architecture

### Public surface (exact match for tkmd, preserves call-site contracts)

```go
package epos

// types.go — unchanged shape
type Status string
const (
    StatusOpen       Status = "open"
    StatusReady      Status = "ready"
    StatusInProgress Status = "in_progress"
    StatusBlocked    Status = "blocked"
    StatusClosed     Status = "closed"
)

type Ticket struct {
    ID                 string
    Title              string
    Status             Status
    Deps               []string
    Priority           int
    AcceptanceCriteria []string
    TestCases          []string
    ValidationCommands []string
    OwnedPaths         []string
    ReviewThreshold    string
    Runtime            string
    Model              string  // deprecated, retained for compat
    Body               string
    UnknownFrontmatter map[string]any

    present      map[string]bool  // unexported, mirrors epos Ticket.Present
    titleDerived bool
}

// store.go — same signatures as tkmd
func LoadTicket(path string) (Ticket, error)
func SaveTicket(path string, ticket Ticket) error
func ListReadyChildren(rootDir, parentID string, currentRunID ...string) ([]Ticket, error)
func ListAllChildren(rootDir, parentID string) ([]Ticket, error)
func HasChildren(rootDir, ticketID string) (bool, error)
func DetectEpicCycle(epicID string, ancestors map[string]struct{}) error
func ValidateTicketSchedulingFields(ticket Ticket) error

// claims.go — same signatures, internals split live (epos) + durable (verk)
func AcquireClaim(rootDir string, args ...any) (state.ClaimArtifact, error)
func RenewClaim(rootDir string, args ...any) (state.ClaimArtifact, error)
func ReleaseClaim(rootDir string, args ...any) error
func ReconcileClaim(live, durable *state.ClaimArtifact, runID string, terminal bool) (state.ClaimArtifact, error)
func ValidateLeaseFence(expected, actual string) error

// live.go — adapter helpers needed because epos live sidecars are RuntimeState,
// not state.ClaimArtifact. These are intentionally not tkmd-compatible.
func LoadLiveClaim(rootDir, ticketID string) (*state.ClaimArtifact, error)
func RuntimeStateToClaimArtifact(rs *eposticket.RuntimeState) *state.ClaimArtifact
```

The core tkmd signatures stay identical. Most call sites are import-path renames once the adapter compiles. The exception is resume/status live-claim reconciliation: those call sites must stop unmarshaling `.tickets/.claims/<ticketID>.json` as `state.ClaimArtifact` and instead call `epos.LoadLiveClaim`, because the live sidecar JSON shape becomes epos `RuntimeState`.

### Schema bridge (verk Ticket ⇄ epos Ticket)

Conversion lives in `convert.go`. Round-trip-safe in both directions provided `present`/`Present` is preserved.

| verk field | epos field | Notes |
|---|---|---|
| `ID` | `Ticket.ID` | direct |
| `Title` | `Ticket.Title` | direct |
| `Status` | `Ticket.Status` (after normalization, see below) | |
| `Deps` | `Ticket.Deps` | direct |
| `Priority` | `Ticket.Priority` | direct |
| `AcceptanceCriteria` | `Ticket.AcceptanceCriteria` | direct |
| `TestCases` | `Ticket.TestCases` | direct |
| `ValidationCommands` | `Ticket.ValidationCommands` | direct |
| `OwnedPaths` | `Ticket.Scope.OwnedPaths` | nested in epos `TaskScope` (inline yaml) |
| `ReviewThreshold` | `Ticket.ReviewThreshold` | direct |
| `Runtime` | `Ticket.RuntimePreference` (yaml key `runtime`) | rename in struct, same on disk |
| `Model` | `Extra["model"]` | epos has no Model field |
| `Body` | raw Markdown body owned by adapter | epos `Ticket` has no raw body field. `LoadTicket` must split and preserve raw body bytes separately from epos parsing; `SaveTicket` must write generated frontmatter plus `Ticket.Body`. Do not rely on `eposmarkdown.MarshalTicket` to reconstruct arbitrary verk body text. |
| `UnknownFrontmatter["parent"]` | `Ticket.Parent` | epos named field; must be copied both directions so verk's existing `parentOf` semantics continue to work. |
| `UnknownFrontmatter["type"]` | `Ticket.Type` | epos named field; must be copied both directions so `type: epic` continues to drive epic-deps child discovery. |
| `UnknownFrontmatter["extended_status"]` | `Ticket.ExtendedStatus` | epos named field; must be copied both directions and preserved when verk normalizes `Status`. |
| `UnknownFrontmatter["model"]` / `Model` | `Ticket.Extra["model"]` | epos has no Model field. Preserve both the compatibility field and unknown frontmatter behavior. |
| other epos named fields unknown to verk (`tags`, `description`, `created`, `updated_at`, `order`, etc.) | epos named fields, mirrored through `UnknownFrontmatter` | These are not in `Ticket.Extra` after epos unmarshal. `fromEpos` must explicitly copy every non-verk epos named field that was present into `UnknownFrontmatter`; `toEpos` must restore them from `UnknownFrontmatter`. |
| truly unknown frontmatter | epos `Ticket.Extra` | Only keys not represented by epos named fields live here. |
| `present` | epos `Ticket.Present` | direct copy |
| `titleDerived` | epos `Ticket.TitleDerived` | direct |

**Named-field bridge rule:** do not treat `UnknownFrontmatter` and epos `Extra` as equivalent maps. epos absorbs many formerly unknown verk keys into named fields. Conversion must explicitly bridge named fields first, then merge true `Extra` keys.

**Status normalization** (read path only for verk's public `Ticket.Status`; write path passes verk values straight through while preserving `extended_status`):

```go
func normalizeStatus(s eposticket.Status) Status {
    switch s {
    case eposticket.StatusPending, eposticket.StatusRepairPending:
        return StatusOpen
    case eposticket.StatusClaimed, eposticket.StatusImplementing,
         eposticket.StatusVerifying, eposticket.StatusUnderReview:
        return StatusInProgress
    case eposticket.StatusDone, eposticket.StatusFailed:
        return StatusClosed
    case eposticket.StatusHeld:
        return StatusBlocked
    }
    return Status(s) // tk-native passes through
}
```

Prefer `eposmarkdown.StatusToTK` where it matches this mapping, but preserve verk's historical `pending`/`repair_pending` → `open` behavior unless an explicit compatibility test approves `ready`. The original `extended_status` value survives in `UnknownFrontmatter["extended_status"]` so a verk-write doesn't clobber fabrikk-authored extended state. (verk `assignField` ignores unknown keys but `decodeFrontMatter` collects them — this behavior must be preserved.)

### Raw body and path-write contract

tkmd `LoadTicket(path)` and `SaveTicket(path, ticket)` are path-oriented APIs. The new adapter must preserve that exact contract:

- `LoadTicket(path)` reads exactly `path`, splits YAML frontmatter from raw Markdown body, parses the full document with `eposmarkdown.UnmarshalTicket`, converts to verk `Ticket`, and sets `Ticket.Body` to the raw body string after the closing frontmatter delimiter.
- `SaveTicket(path, ticket)` writes exactly `path`. It must not silently redirect to `<root>/.tickets/<ticket.ID>.md` unless `path` is already that path.
- If `path` already exists, use epos frontmatter generation/update plus `Ticket.Body` so arbitrary body text survives. `eposmarkdown.UpdateFrontmatter` can preserve an existing body, but when `ticket.Body` has changed the adapter must render frontmatter and append `ticket.Body` directly.
- If `path` does not exist, create parent directories as tkmd did through atomic write behavior and write frontmatter plus `ticket.Body`.
- Keep verk's `validateTicketWritable` behavior: reject invalid statuses and invalid `owned_paths` before writing.

Implementation note: `eposstore.FileStore.Update` is not the default for this adapter because it is ID/root-oriented and regenerates body sections when rich fields change. Prefer path-preserving frontmatter generation plus `state.SaveFileAtomic` unless epos exposes a path-preserving helper before implementation.

### Claim split (live = epos, durable = verk)

Live sidecar `.tickets/.claims/<ticketID>.json` is owned by `eposruntime`. Durable archive `.verk/runs/<runID>/claims/claim-<ticketID>.json` is owned by verk. Adapter functions sequence the two:

- **AcquireClaim:**
  1. Validate identifiers via verk's local path-containment helpers (port them into the new package; live-sidecar paths are already protected by epos's internal safepath, only durable-archive paths need verk-side checks).
  2. Preserve caller-supplied `leaseID` from the current variadic API when present. Generate `leaseID := "lease-<runID>-<ticketID>-<nanos>"` only when the caller did not provide one. Existing engine call sites pass explicit wave/run fence values; those must remain the single source of truth.
  3. Call `eposruntime.Claim(rootDir, ticketID, ownerRunID, runID, ttl, eposruntime.WithLeaseID(leaseID))`. Mapping: verk `OwnerRunID` → epos `Claim.ClaimedBy`; verk `runID` → epos `Claim.ClaimBackend`; verk `LeaseID` → epos `Lease.LeaseID` (one-to-one since commit `65248f2`).
  4. Read the live `RuntimeState` back with `eposruntime.ReadRuntimeState` and convert it to a `state.ClaimArtifact`. Use the epos lease expiry as the durable `ExpiresAt`; use caller-provided `now` only for verk artifact metadata where exact live timestamps are unavailable.
  5. Atomically write durable archive under `.verk/runs/<runID>/claims/`.

- **RenewClaim:** verify fence against both durable `LeaseID` and live `RuntimeState.Lease.LeaseID`; call `eposruntime.Renew(rootDir, ticketID, ownerRunID, ttl, eposruntime.WithLeaseID(currentLeaseID))` so the live sidecar keeps the same fence value; read the renewed live state back and rewrite durable. If durable rewrite fails after live renew succeeds, report the divergence; there is no safe way to roll back epos's live lease without another runtime mutation.

- **ReleaseClaim:** call `eposruntime.Release(rootDir, ticketID, ownerRunID, eposticket.StatusPending, releaseReason)`; mark durable as `State="released"` + write `ReleasedAt` + `ReleaseReason`. epos release leaves a `.tickets/.claims/<ticketID>.json` RuntimeState sidecar with no active `Claim` and `status: pending`; it does not remove the file like tkmd did. Tests and smoke inspection must assert this new live-sidecar behavior.

- **Compatibility mode:** if the durable archive exists but the live epos `RuntimeState` is missing, the durable archive may recover or release missing live state after owner and lease-fence validation. Renew may recreate the live sidecar from the active durable claim; release may write a released/pending epos `RuntimeState` and mark durable released. This preserves resilience across interrupted migrations and missing sidecars while keeping durable owner/lease validation mandatory.

- **LoadLiveClaim / RuntimeStateToClaimArtifact:** convert epos live sidecar state to the verk claim shape expected by `ReconcileClaim`. Return `nil, nil` when there is no active epos `Claim`. For active claims, map `TicketID`, `OwnerRunID = RuntimeState.Claim.ClaimedBy`, `LeaseID = RuntimeState.Lease.LeaseID`, `LeasedAt = RuntimeState.Claim.ClaimedAt`, `ExpiresAt = RuntimeState.Lease.ExpiresAt`, `State = "active"`, and `LastSeenLiveClaimPath = .tickets/.claims/<ticketID>.json`.

- **ReconcileClaim:** keep the existing `state.ClaimArtifact` reconciliation logic, but call sites must feed it live artifacts produced by `LoadLiveClaim`, not `loadOptionalClaim(liveClaimPath(...))`.

**Lease ID format:** verk's fence value is the single source of truth on both sides. The durable archive holds it; the live sidecar receives the same value via `WithLeaseID`. `ValidateLeaseFence` succeeds only when the caller-supplied fence matches what was written end-to-end.

### Path containment

Port verk's symlink-aware containment helpers (`assertPathUnderBase`, `resolveBaseForContainment`, etc.) into the new `epos` package as-is for durable-archive paths. epos `v0.2.0` hardens live sidecar paths internally, but verk still owns `.verk/runs/<runID>/claims/...` containment. Long-term: extract into a shared `verk/internal/security` package; out of scope for this migration.

---

## Phase Breakdown

Estimate: **6 sessions, ~3–6 hours of agent time each**. Each phase ends in a green repo gate: `just pre-commit` for focused changes, `just test-race` for orchestration/isolation changes, and `just check` before merge.

### Phase 1 — Pre-flight + dependency add (~1 session)

1. Stash/commit verk's pending `AGENTS.md` change.
2. Confirm `github.com/php-workx/epos@v0.2.0` is published and contains commits `65248f2`, `56a310e`, and `f9ecd4f`. Do not start implementation against `v0.1.0`.
3. `cd $VERK_ROOT && go get github.com/php-workx/epos@v0.2.0`.
4. Add a sentinel import in `verk/internal/adapters/ticketstore/epos/doc.go` (new file, just `_ "github.com/php-workx/epos/ticket"` blank import) so `go mod tidy` keeps the dep until real usage exists.
5. `go mod tidy`, then `just pre-commit` — must stay green.
6. Create new package directory `verk/internal/adapters/ticketstore/epos/` with stub files: `doc.go`, `types.go`, `store.go`, `claims.go`, `convert.go`, `containment.go`, `live.go`. No exported behavior yet beyond compile-safe stubs if needed.
7. Commit: `chore(deps): add epos v0.2.0 + adapter package skeleton`.

**Exit:** `just pre-commit` green; new package compiles; tkmd untouched.

### Phase 2 — Types, conversion, schema tests (~1 session)

1. In `epos/types.go`: copy the public `Ticket` + `Status` shape from tkmd verbatim (so the package is a structural superset).
2. In `epos/convert.go`: implement `toEpos(Ticket) *eposticket.Ticket` and `fromEpos(*eposticket.Ticket) Ticket`. Handle the `OwnedPaths` ↔ `Scope.OwnedPaths` move, status normalization, `Model` ↔ `Extra["model"]`, `Present` map mirroring, and explicit bridging for epos named fields that verk stores in `UnknownFrontmatter`.
3. In `epos/types_test.go`: round-trip tests
   - verk → epos → verk (lossless for 12 core keys)
   - epos with extended_status → verk: status normalized, extended_status preserved in `UnknownFrontmatter`
   - verk write → epos read → verk read (full Body/Title/UnknownFrontmatter survives)
   - parent field handling: epos has `Parent` named field; verk stores it in `UnknownFrontmatter["parent"]`. Fabrikk-authored ticket with `parent: epic-X` must be visible in verk's `UnknownFrontmatter`.
   - type field handling: epos has `Type` named field; `type: epic` must survive in `UnknownFrontmatter["type"]` for verk epic behavior.
   - named metadata handling: `created`, `updated_at`, `order`, and one truly unknown key must round-trip without being dropped or duplicated.
4. Commit: `feat(adapter): epos↔verk Ticket conversion + round-trip tests`.

**Exit:** new package's unit tests pass; tkmd still in use everywhere.

### Phase 3 — LoadTicket / SaveTicket (~1 session)

1. Implement `epos.LoadTicket` using path-preserving file read + frontmatter/body split + `eposmarkdown.UnmarshalTicket` + `convert.fromEpos`. Set `Ticket.Body` to the exact raw body after the closing frontmatter delimiter.
2. Implement `epos.SaveTicket` as a path-preserving write. Do not use `eposstore.FileStore.Update` as the default because it is root/ID-oriented and may regenerate body sections. Generate/update frontmatter from `convert.toEpos(ticket)`, append `ticket.Body`, and write exactly `path` via `state.SaveFileAtomic`.
3. Re-port `extractHeadingTitle` test cases and confirm parity with `eposmarkdown.ExtractHeadingTitle`.
4. Re-port relevant tests from `tkmd/store_test.go` covering Load/Save round-trips into `epos/store_test.go`. Skip tests that probe tkmd-internal helpers (`decodeFrontMatter`, `asInt`, etc.) — those don't apply.
5. Add path-contract tests:
   - saving to an arbitrary temp output path writes that exact path
   - saving a ticket with raw non-structured body preserves the body byte-for-byte
   - saving a ticket with changed `Body` writes the new body, not epos-rendered sections
6. Commit: `feat(adapter): epos LoadTicket/SaveTicket via epos library`.

**Exit:** new `epos.LoadTicket`/`epos.SaveTicket` tests pass; tkmd still in use.

### Phase 4 — Listing + scheduling helpers (~1 session)

1. `epos.ListAllChildren` → adapter-owned implementation using epos parsing plus tkmd-compatible child membership:
   - return an error if `<root>/.tickets/<parentID>.md` does not exist
   - include direct children with `parent: <parentID>`
   - if the parent has `type: epic`, also include tickets whose ID appears in the parent epic's `deps`
   - never treat normal task deps as child edges
   - de-duplicate by ticket ID and preserve sorted file order
2. `epos.ListReadyChildren(rootDir, parentID, currentRunID...)` →
   - Use the same tkmd-compatible child membership as `ListAllChildren`; do not call `eposstore.FileStore.FilterReadyChildren` directly because it ignores `status: ready` and epic-deps child membership.
   - Normalize each child's public verk status before readiness checks.
   - Treat `StatusOpen` and `StatusReady` as ready, matching tkmd.
   - Treat deps as scheduling deps only; every dep must exist and normalize to `StatusClosed`.
   - Get the live-claim set from `store.ActiveClaimSet()` (since epos `v0.2.0`, this is one call, not a manual sidecar scan).
   - Add verk's durable active claims: walk `.verk/runs/*/claims/*.json`, union into `claimed`, and ignore released/expired durable artifacts.
   - Drop entries owned by the supplied `currentRunID` (so a run still sees its own claims as available for re-acquisition).
3. `epos.HasChildren` → call adapter-owned `ListAllChildren` and return `len > 0`.
4. `epos.DetectEpicCycle` → keep tkmd's ancestor-set semantics exactly. It only needs to detect whether `epicID` already appears in the provided `ancestors`; do not replace it with full epos graph cycle detection unless callers are changed to pass the whole graph.
5. `epos.ValidateTicketSchedulingFields` → preserve tkmd validation exactly: valid verk status plus verk's `owned_paths` validation. Do not use a bare epos wrapper if it accepts broader schema values.
6. Re-port relevant tests, including:
   - `status: ready` appears in `ListReadyChildren`
   - parent-backed child appears
   - epic-deps-backed child appears only when parent is `type: epic`
   - normal task deps are not treated as child edges
   - durable-archive-only active claim excludes a ticket, except for `currentRunID`
7. Commit: `feat(adapter): listing + scheduling helpers via epos primitives`.

**Exit:** all read-path adapter functions pass tests; tkmd still in use at call sites.

### Phase 5 — Claims (live via epos, durable in verk) (~1 session)

The epos `ticket/internal/safepath` package is unreachable from outside the epos module, so verk still ports its own copies of `assertPathUnderBase` etc. for the durable-archive paths. The live-sidecar path is now hardened upstream (commit `56a310e`) and needs no extra protection.

1. Port path-containment helpers (`assertPathUnderBase`, `assertPathUnderIntendedBase`, `resolveBaseForContainment`, `resolveIntendedBaseForContainment`, `resolvePathForContainment`, `resolvePathForContainmentAllowMissingAncestors`, `validateClaimIdentifier`) into `epos/containment.go`. Apply only to durable-archive paths under `.verk/runs/...`.
2. Implement `epos.AcquireClaim`:
   - Parse variadic args (port `parseAcquireClaimRequest` and rename `acquireClaimRequest` → unexported struct).
   - Preserve `req.leaseID` when supplied. Generate the verk-style fence only when missing: `leaseID := "lease-" + runID + "-" + ticketID + "-" + strconv.FormatInt(now.UnixNano(), 10)`.
   - Call `eposruntime.Claim(rootDir, ticketID, ownerRunID, runID, ttl, eposruntime.WithLeaseID(leaseID))`. The mapping is: verk's `OwnerRunID` → epos `ClaimedBy`; verk's `runID` → epos `ClaimBackend`; verk's `LeaseID` → epos `Lease.LeaseID` directly (no translation needed since commit `65248f2`).
   - Read epos `RuntimeState` back and convert it into `state.ClaimArtifact` with `RuntimeStateToClaimArtifact`.
   - Fill verk-only fields (`OwnerWaveID`, artifact metadata, `LastSeenLiveClaimPath`) and atomically write the durable archive under `.verk/runs/<runID>/claims/`.
3. Implement `epos.RenewClaim`:
   - Parse args (port `parseRenewClaimRequest`).
   - Load live via `eposruntime.ReadRuntimeState`, load durable via verk code, and validate the caller's fence against both live and durable lease IDs.
   - Call `eposruntime.Renew(rootDir, ticketID, ownerRunID, ttl, eposruntime.WithLeaseID(currentLeaseID))` so the live sidecar keeps the fence value the durable archive already records.
   - Read the renewed live state back, convert to durable shape, preserve `OwnerWaveID`, and rewrite durable.
4. Implement `epos.ReleaseClaim`:
   - Parse args (port `parseReleaseClaimRequest`).
   - Load live/durable first and validate owner + lease fence before mutating epos runtime.
   - Call `eposruntime.Release(rootDir, ticketID, ownerRunID, eposticket.StatusPending, releaseReason)`.
   - Update durable to `State="released"` + write `ReleasedAt` + `ReleaseReason`.
   - Do not remove `.tickets/.claims/<ticketID>.json`; epos owns that file and keeps released runtime state as `RuntimeState` with no active claim.
5. Implement `epos.LoadLiveClaim` and `epos.RuntimeStateToClaimArtifact` in `live.go`; these are the bridge for status/resume code that still wants `state.ClaimArtifact`.
6. Implement `epos.ReconcileClaim` — port the pure reconciliation logic from tkmd, operating on already-converted `state.ClaimArtifact` values.
7. Implement `epos.ValidateLeaseFence` — copy from tkmd verbatim (3 lines). Identical semantics now that fence values round-trip end-to-end through epos.
8. Re-port `claims_test.go` (776 lines, 17 tests). The live-sidecar tests need to assert against epos's `RuntimeState` JSON shape rather than tkmd's `ClaimArtifact`; durable tests are unchanged.
9. Add focused bridge tests:
   - `AcquireClaim` with caller-supplied lease preserves that exact live and durable lease ID
   - generated lease is used only when caller supplied none
   - `LoadLiveClaim` returns nil for missing or released/no-claim RuntimeState
   - `LoadLiveClaim` converts active RuntimeState to `state.ClaimArtifact`
   - `ReleaseClaim` leaves epos RuntimeState sidecar present with no active claim
10. Commit: `feat(adapter): claims (live=epos, durable=verk)`.

**Exit:** new package fully implements tkmd's surface; tkmd still in use at call sites; epos-side tests for the new package pass.

### Phase 6 — Cutover: rename imports, update live readers, delete tkmd (~1 session)

1. Run a mechanical find/replace across the tkmd import call sites:
   - `verk/internal/adapters/ticketstore/tkmd` → `verk/internal/adapters/ticketstore/epos`
   - `tkmd.` → `epos.`
2. Update resume/status live-claim reads:
   - `internal/engine/status.go` `deriveTicketClaim`: replace `loadOptionalClaim(liveClaimPath(...))` with `epos.LoadLiveClaim(repoRoot, ticketID)`.
   - `internal/engine/resume.go` `reconcileTicketClaimForResume`: replace `loadOptionalClaim(liveClaimPath(...))` with `epos.LoadLiveClaim(repoRoot, ticketID)`.
   - Keep durable reads through `loadOptionalClaim(durableClaimPath(...))`.
3. Update `pkg/verk/types.go:65` from `type Ticket = tkmd.Ticket` to `type Ticket = epos.Ticket`. Same struct shape, alias preserved.
4. Run the repo gate from `$VERK_ROOT`: `just pre-commit`. All integration tests must pass without tkmd.
5. If any test pulls tkmd-internal helpers (`decodeFrontMatter`, `asInt`, etc.), it must be either:
   - rewritten to test public behavior, or
   - deleted because the new adapter doesn't expose them (per Phase 1 inventory: only tkmd's own tests do this, not engine/cli/e2e tests — should be a no-op).
6. Delete `verk/internal/adapters/ticketstore/tkmd/` entirely (4 source files + 2 test files + 2 lock files = 8 files).
7. Run `just test-race` because this migration touches orchestration and isolation behavior.
8. Run `just check` (verk's full quality gate).
9. Commit: `refactor!: replace tkmd with epos adapter`. Mention BREAKING in body if external consumers depend on tkmd directly (none expected, but worth a `go doc verk` sanity check before merge).

**Exit:** tkmd deleted; verk builds + tests green; epos backs all ticket I/O.

---

## Critical Files

### epos library (read-only, reuse heavily)
- `$EPOS_ROOT/ticket/tickets.go` — canonical Ticket struct + functional options + Status constants
- `$EPOS_ROOT/ticket/markdown/frontmatter.go` — `MarshalTicket`, `UnmarshalTicket`, `UpdateFrontmatter`, `UpdateBody`
- `$EPOS_ROOT/ticket/markdown/body.go` — `ExtractHeadingTitle`, `RenderSections`, `AddNote`
- `$EPOS_ROOT/ticket/markdown/compat.go` — `StatusToTK`, `StatusFromTK`, `StatusToExtended` — use where compatible, but preserve verk's explicit normalization tests if mappings differ
- `$EPOS_ROOT/ticket/store/store.go` — `FileStore`, `Create`/`Read`/`Update`/`Delete`/`List`/`ResolveID`/`AddNote`/`AddDep`/`RemoveDep`/`Link`/`Unlink`/`ListAllChildren`/`FilterReadyChildren`. Use read/CRUD primitives, but do not blindly wrap child/ready filters where verk compatibility differs.
- `$EPOS_ROOT/ticket/store/claims.go` — `ActiveClaimSet`, `ListReady`
- `$EPOS_ROOT/ticket/runtime/claim.go` — `Claim`/`Release`/`Renew`/`ReclaimExpired`/`ReadClaimsForRun`/`ClaimAllowsReady`/`ReadRuntimeState`/`WriteRuntimeState`
- `$EPOS_ROOT/ticket/runtime.go` — `RuntimeState`, `Claim`, `Lease`, `Heartbeat`; source shape for `LoadLiveClaim`
- `$EPOS_ROOT/ticket/graph/deps.go` — `ReadyFilter`/`BlockedFilter`/`DetectCycles`/`FilterChildren`/`FilterReadyChildren`. Use as reference only where it preserves verk behavior.

### verk source (modify)
- `$VERK_ROOT/go.mod` — add epos dep (Phase 1)
- `$VERK_ROOT/internal/adapters/ticketstore/epos/` — new package (Phases 1–5)
- `$VERK_ROOT/internal/adapters/ticketstore/tkmd/` — delete (Phase 6)
- `$VERK_ROOT/pkg/verk/types.go:65` — update type alias (Phase 6)
- 28 call sites for import-path rename (Phase 6). Current inventory from `rg -l "internal/adapters/ticketstore/tkmd" --glob '*.go' .`; refresh before cutover. Top-volume files:
  - `internal/cli/run.go`
  - `internal/engine/ticket_run.go` (1280, 1518)
  - `internal/engine/resume.go` (90, 222, 228, 374, 874)
  - `internal/engine/epic_run.go` (277, 949, 1211, 1479)
  - `internal/engine/epic_gate.go` (30)
  - `internal/engine/intake.go` (8)
  - `internal/engine/ops_support.go` (11, 457)
  - `internal/engine/closeout.go`, `status.go`, `wave_scheduler.go`
  - `internal/e2e/*_test.go` (3 files)
  - `cmd/verk/main_test.go`, `internal/cli/run_*_test.go`

### verk source (read-only, source of truth for porting)
- `$VERK_ROOT/internal/state/types.go:388-400` — `state.ClaimArtifact` definition
- `$VERK_ROOT/internal/adapters/ticketstore/tkmd/claims.go` — claim/lease state machine; port AcquireClaim/RenewClaim/ReleaseClaim/ReconcileClaim
- `$VERK_ROOT/internal/adapters/ticketstore/tkmd/store.go` — frontmatter codec (mostly replaceable by epos), raw body/path contract (must preserve), `extractHeadingTitle` (parity check vs epos), `validateOwnedPath` (port), `isChildOf`/`loadEpicChildren`/`depsClosed` (port semantics), `claimAllowsReady` (replace with epos live claim set plus durable archive logic)
- `$VERK_ROOT/internal/adapters/ticketstore/tkmd/types.go` — Ticket + Status; mirror in new package

---

## Verification

### Per-phase
- `just pre-commit` after each phase.
- `go test ./internal/adapters/ticketstore/epos/...` after Phases 2–5 for fast adapter feedback.
- `just test-race` after Phase 6 because the cutover touches orchestration and isolation behavior.
- After Phase 3, explicitly inspect a saved fixture to confirm arbitrary raw body text is unchanged and the file path did not change.
- After Phase 4, run focused tests for both parent-backed and epic-deps-backed child discovery.
- After Phase 6, run resume/status focused tests that exercise live epos RuntimeState sidecars.

### End-to-end gates
- `just check` (verk's pre-push gate: format, vet, lint, race tests, govulncheck, semgrep) must pass on the cutover commit.
- Manual smoke: run a verk session against a fixture `.tickets/` dir; verify it preserves tkmd-visible behavior for a non-trivial workflow (claim → resume → release → close). Expected on-disk difference: live `.tickets/.claims/<id>.json` now uses epos `RuntimeState` shape.
- epos sidecar inspection: `cat .tickets/.claims/<id>.json` after Acquire shows active epos `RuntimeState` with `claim` and `lease`, not tkmd's `ClaimArtifact`.
- epos release inspection: `cat .tickets/.claims/<id>.json` after Release shows epos `RuntimeState` with no active `claim`, not a removed file.
- Durable archive inspection: `cat .verk/runs/<runID>/claims/claim-<id>.json` shows verk's `state.ClaimArtifact` unchanged.
- Cross-tool: a verk-written ticket should still parse cleanly via `epos show <id> --json`.

### Rollback
- Each phase commits independently. Phase 6 is the only commit that deletes tkmd.
- If Phase 6 reveals breakage, revert Phase 6 only — Phases 1–5 leave the new package alongside tkmd, no behavior change at call sites.

---

## Open Follow-ups (out of scope, file as epos tickets after Phase 6)

1. ~~`epos ready` is sidecar-blind.~~ **Resolved** in commit `f9ecd4f` — `store.ListReady` and `graph.ReadyFilterUnclaimed` now hide actively-claimed tickets; `--include-claimed` is the debug escape hatch. Verk's adapter still merges the durable archive on top.
2. **fabrikk integration revival.** Same drop-in pattern as the verk rename. Blocked at the moment by uncommitted llmcli/ work in fabrikk; once that lands, a single anchor import + adapter scaffold reactivates Plan E5.
3. ~~Lease-ID format.~~ **Resolved** in commit `65248f2` — `eposruntime.WithLeaseID` accepts caller-supplied fence values. Phase 5 uses it directly; no shim required.
4. ~~Path-containment hardening in epos.~~ **Partially resolved** in commit `56a310e` — `ticket/internal/safepath` protects every sidecar I/O. Still TODO: expose a public `ticket/safepath` once a second consumer needs the helpers outside the epos module (e.g. fabrikk's adapter for its own non-epos paths).
5. **Skill catalog.** epos skill currently advertises a 12-command surface targeting agents. With verk integrated, no skill change needed — verk doesn't shell out to `epos`, it links the library.
6. **Stale runtime locks.** `.tickets/.claims/*.lock` files accumulate (zero-byte, flock leaves them on disk). Not harmful but worth a sweep, particularly after deleting the parent ticket. Track as new epos ticket post-cutover.

---

## Handoff Notes for Fresh Agent

- **Start here:** read `IMPLEMENTATION_PLAN.md` E6, then this file, then the pre-mortem report at `.agents/council/2026-05-08-pre-mortem-epos-integration.md`. Refresh the tkmd call-site inventory with `rg` before Phase 6.
- **Per-session checkpoint:** at the end of each phase, run `git log --oneline -5` in both repos to confirm clean commit boundaries; run `just pre-commit` in verk to confirm green.
- **Don't merge phases.** Each commit must be revertible.
- **Don't touch fabrikk** during this migration. fabrikk integration is a separate epic.
- **Don't expand epos `RuntimeState`.** User-approved decision: keep epos lean, durable archive stays in verk. Only revisit if Phase 5 surfaces a hard blocker.
- **Use `eposruntime.WithLeaseID`** for every Claim/Renew/ReclaimExpired call in the adapter. Do not store verk's lease ID only in the durable archive — the live sidecar must carry the same fence value so concurrent consumers see one truth.
- **Preserve caller-supplied lease IDs.** Generate a new lease only when the current variadic API did not provide one.
- **Do not treat the cutover as purely mechanical.** Resume/status live-claim reads must call `epos.LoadLiveClaim` because the live sidecar JSON shape changes.
- **Do not wrap epos child/ready filters blindly.** Preserve verk's `status: ready` and epic-deps child semantics in the adapter.
- **Preserve raw body and path writes.** `SaveTicket(path, ticket)` writes exactly `path` and appends `ticket.Body`; do not let epos regenerate arbitrary body text.
- **`ticket/internal/safepath` is internal** and not importable from verk. Keep verk's local containment helpers; they protect durable-archive paths only. Live-sidecar paths are protected by epos automatically.
- **Don't rename status enum**. Verk keeps its 5-state set; the adapter normalizes on read. Resist the urge to "harmonize."
- **Caveman mode is on by default in this user's session.** Code/commits/security messages in normal English; chat output terse fragments.
