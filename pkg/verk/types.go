// Package verk provides the exported engine API for embedding verk into other
// applications (e.g. Fabrikk/Fabric). It is the only public entry point; all
// engine logic lives in internal/engine and is delegated to, not duplicated.
package verk

import (
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/engine"
	"verk/internal/policy"
	"verk/internal/state"
)

// ──────────────────────────────────────────────
// Type re-exports — engine request/result types
// ──────────────────────────────────────────────

// RunTicketRequest configures a single-ticket run.
type RunTicketRequest = engine.RunTicketRequest

// RunTicketResult is the outcome of a single-ticket run.
type RunTicketResult = engine.RunTicketResult

// TicketRunSnapshot captures the state of a ticket within a run.
type TicketRunSnapshot = engine.TicketRunSnapshot

// RunEpicRequest configures an epic (multi-ticket) run.
type RunEpicRequest = engine.RunEpicRequest

// RunEpicResult is the outcome of an epic run.
type RunEpicResult = engine.RunEpicResult

// ResumeRequest configures a run resume operation.
type ResumeRequest = engine.ResumeRequest

// ResumeReport is the outcome of a resume operation.
type ResumeReport = engine.ResumeReport

// ReopenRequest configures a ticket reopen operation.
type ReopenRequest = engine.ReopenRequest

// StatusRequest configures a status derivation.
type StatusRequest = engine.StatusRequest

// StatusReport is the derived status of a run.
type StatusReport = engine.StatusReport

// StatusTicket is a per-ticket entry within a StatusReport.
type StatusTicket = engine.StatusTicket

// DoctorReport is the health-check output from RunDoctor.
type DoctorReport = engine.DoctorReport

// DoctorCheck is a single health check within a DoctorReport.
type DoctorCheck = engine.DoctorCheck

// RuntimeCheck records runtime availability within a DoctorReport.
type RuntimeCheck = engine.RuntimeCheck

// ──────────────────────────────────────────────
// Type re-exports — domain types needed by callers
// ──────────────────────────────────────────────

// Ticket is a parsed ticket from the ticket store.
type Ticket = tkmd.Ticket

// Adapter is the runtime adapter interface (Codex, Claude, etc.).
type Adapter = runtime.Adapter

// Config is the engine policy/runtime configuration.
type Config = policy.Config

// SchedulerConfig holds wave scheduler settings.
type SchedulerConfig = policy.SchedulerConfig

// PolicyConfig holds review/retry policy settings.
type PolicyConfig = policy.PolicyConfig

// RuntimeConfig holds runtime adapter settings.
type RuntimeConfig = policy.RuntimeConfig

// VerificationConfig holds verification command settings.
type VerificationConfig = policy.VerificationConfig

// LoggingConfig holds logging settings.
type LoggingConfig = policy.LoggingConfig

// ──────────────────────────────────────────────
// Type re-exports — state/artifact types
// ──────────────────────────────────────────────

// TicketPhase represents the lifecycle phase of a ticket.
type TicketPhase = state.TicketPhase

// EpicRunStatus represents the overall status of an epic run.
type EpicRunStatus = state.EpicRunStatus

// WaveStatus represents the status of a wave within an epic run.
type WaveStatus = state.WaveStatus

// RunArtifact is the persisted run state.
type RunArtifact = state.RunArtifact

// WaveArtifact is the persisted wave state.
type WaveArtifact = state.WaveArtifact

// PlanArtifact is the persisted plan for a ticket.
type PlanArtifact = state.PlanArtifact

// ClaimArtifact is the persisted claim/lease state.
type ClaimArtifact = state.ClaimArtifact

// CloseoutArtifact is the persisted closeout decision.
type CloseoutArtifact = state.CloseoutArtifact

// Severity represents finding severity levels (P0–P4).
type Severity = state.Severity

// ArtifactMeta is the common metadata header for all artifacts.
type ArtifactMeta = state.ArtifactMeta
