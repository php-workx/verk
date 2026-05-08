# AGENTS.md

`AGENTS.md` is the durable repo instruction file. Do not put session memory,
ticket transcripts, or scratch notes here. Use `.agents/local-context.md` for
ephemeral local context instead.

## What This Repo Is

`verk` is an execution engine for ticketed implementation work. It operates on
file-backed tasks, runs deterministic local gates, and supports longer-running
execution patterns such as worktree-backed ticket isolation.

## Hard Rules

- Keep this file stable. Never inline `<claude-mem-context>
# Memory Context

# [verk] recent context, 2026-04-27 1:56pm GMT+2

Legend: 🎯session 🔴bugfix 🟣feature 🔄refactor ✅change 🔵discovery ⚖️decision 🚨security_alert 🔐security_note
Format: ID TIME TYPE TITLE
Fetch details: get_observations([IDs]) | Search: mem-search skill

Stats: 50 obs (20,909t read) | 764,278t work | 97% savings

### Apr 25, 2026
1824 4:00p 🔵 tk close Updates Ticket Status In-Memory Only — No Git-Tracked Diff in .tickets/ Files
1826 4:02p ⚖️ ver-gjpq: Wave Ordinal Persistence Race Condition Fix — Design Scoped and Implementation Started
1832 4:04p 🟣 ver-wi Worker 1: Engine Worktree Safety and Security Hardening — Session Started
1833 " 🟣 ver-wi Worker 1: Engine Worktree Safety Hardening — All Acceptance Tests Pass
1838 4:06p ⚖️ ver-wi: Worker-Isolation Review Gap Closure — Implementation Plan Scoped
1849 4:11p 🟣 ver-0egh + ver-szmt: Wave Integration and Main Apply Safety — Session Started
1856 4:14p 🟣 ver-wi Worker 3: Post-Repair Delta and Main-Apply Recovery — Session Started
1867 4:18p 🟣 ver-wi Worker 3: Post-Repair Delta and Main-Apply Recovery — Session Started
1875 4:21p ⚖️ ver-wi Worker 3: ver-0egh and ver-szmt Implementation Scope Defined
1876 4:22p 🟣 ver-0egh + ver-szmt: Integration Base Rollback and Final Delta Apply Implemented
1879 4:23p 🟣 ver-wi Worker 3: Post-Repair Delta and Base Advancement Safety Fix — Session Scoped
1881 " 🔵 worktree.go: integrationCommit changes is []mergeToMainChange, not []string — gitAddAllPaths type mismatch
1882 " 🔵 mergeToMainChange struct fields: srcRel, destRel, oldRel, kind, mode
1883 " 🔵 mergeToMainChange has symlinkDest field — complete struct definition
1885 4:27p 🟣 ver-wi Worker 3: Post-Repair Integration Delta and Main-Apply Failure Recovery — Session Started
1956 5:36p 🟣 verk ver-tk9k + ver-ber2: Resume Engine Hardening — Worktree Cleanup and Diff Persistence Error Surfacing
1957 " 🟣 ver-quei: Wave Integration Completion Made Recoverable After Final Run Save Failure
1958 " 🔴 ver-imn9: Fresh RunEpic Now Fails Loudly on Blocked-Ticket Diff Artifact Persistence Failure
1959 " 🔵 verk Engine: WaveIntegrationManager Cleanup Gap in resumeEpicMode vs RunEpic
1961 " 🔵 verk Engine Test Infrastructure: Key Patterns for New Resume Tests
1960 " 🔵 completePendingWaveIntegrationTransaction: Exact Code Path for ver-quei Fix Target
1969 5:40p 🟣 ver-tk9k: Resume Integration Worktree Cleanup After Wave Integration
1970 " 🔴 ver-ber2: Resume Fails Loudly on Blocked-Ticket Diff Persistence Failure
1971 " 🟣 ver-quei: Wave Integration Completion Made Recoverable After Final Run Save Failure
1972 " 🔴 ver-imn9: Fresh RunEpic Now Fails Loudly on Blocked-Ticket Diff Artifact Persistence Failure
1973 " 🔴 ver-tk9k + ver-ber2: resumeEpicMode Integration Worktree Cleanup and Diff Persistence Error Surfacing
1977 5:44p 🟣 ver-quei: Wave Integration Completion Made Recoverable After Final Run Save Failure
1978 " 🟣 ver-imn9: Fresh RunEpic Now Fails Loudly on Diff Artifact Persistence Failure
1986 5:49p 🟣 ver-quei: Wave Integration Completion Made Recoverable After Final Run Save Failure
1987 " 🔴 ver-imn9: Diff Artifact Persistence Failures Now Fatal in Fresh RunEpic
1988 5:50p 🟣 ver-quei: Wave Integration Completion Made Recoverable After Final Run Save Failure
1989 " 🔴 ver-imn9: RunEpic Now Fails Loudly on Blocked-Ticket Diff Artifact Persistence Failure
1990 " ✅ epic_run_test.go: WaveArtifact SchemaVersion Hardcoded to 1 in Recovery Test
1991 " 🔵 ver-quei + ver-imn9: Initial Test Run Reveals Two Distinct Root Causes
1993 5:51p 🔵 applyIntegrationCommitToMain: Three-Stage Git Apply Pipeline in epic_run.go
1994 " 🔴 ver-imn9 Implemented: Diff Artifact Persistence Failures Now Surface as Hard Errors in epic_run.go
1995 5:52p 🟣 ver-quei Implemented: completeAlreadyAppliedPendingWaveIntegration Idempotency Guard in wave_verify.go
1996 5:53p ✅ wave_verify.go: Removed Duplicate Nil Integration Manager Guard in completePendingWaveIntegrationTransaction
1998 " 🟣 ver-quei + ver-imn9: Both Regression Tests Pass — ok verk/internal/engine
1999 5:54p 🟣 ver-quei + ver-imn9: Wave Integration Recovery and Diff Persistence Error Surface — Session Started
2011 5:57p 🟣 ver-quei + ver-imn9: Wave Integration Recovery and Diff Persistence Error Surface
2013 " 🔴 ver-quei + ver-imn9: Wave Integration Recovery and Diff Persistence Error Surface — Implemented and Tests Pass
### Apr 27, 2026
2047 8:52a 🔵 JourneySecurity Pulumi Stack: Preview Shows 1 GitHub Provider Update, 292 Unchanged
2048 8:54a 🔵 Pulumi Preview: compliance-stack-Management — No Changes, 97 Resources Unchanged
2049 8:55a 🔵 Pulumi OjinTest Stack Preview: 64 Resources Unchanged, No Drift
S630 Pulumi Compliance Stack OjinDev Preview: GCP VPC Flow Logs Config Update (Apr 27 at 8:55 AM)
2050 8:57a 🔵 Pulumi Compliance Stack OjinDev Preview: GCP VPC Flow Logs Config Update
S631 Pulumi Preview Analysis for compliance-stack-OjinDev — risk assessment and proceed/halt decision (Apr 27 at 8:57 AM)
S632 Pulumi Compliance Stack JourneeTest: Preview Shows 1005 Unchanged Resources (Apr 27 at 8:57 AM)
2051 8:59a 🔵 Pulumi Compliance Stack JourneeTest: Preview Shows 1005 Unchanged Resources
S633 Pulumi Preview Analysis for compliance-stack-JourneeTest — AI decision on whether to proceed (Apr 27 at 8:59 AM)
S634 Pulumi Preview: compliance-stack-JourneeDev — No Infrastructure Changes (Apr 27 at 8:59 AM)
2052 9:02a 🔵 Pulumi Preview: compliance-stack-JourneeDev — No Infrastructure Changes
S635 Pulumi Preview Analysis: compliance-stack-JourneeDev — assess and render proceed/halt/needs_human decision (Apr 27 at 9:02 AM)
S636 Pulumi compliance-stack-JourneeProd Preview: 882 Resources Unchanged (Apr 27 at 9:02 AM)
2053 9:03a 🔵 Pulumi compliance-stack-JourneeProd Preview: 882 Resources Unchanged
S637 Pulumi Preview Analysis for compliance-stack-JourneeProd — decision to proceed or halt (Apr 27 at 9:03 AM)
S638 OjinProd Compliance Stack Pulumi Preview: Single GCP VPC Flow Logs Update (Apr 27 at 9:04 AM)
2054 9:05a 🔵 OjinProd Compliance Stack Pulumi Preview: Single GCP VPC Flow Logs Update
S639 Pulumi Preview Analysis for OjinProd Compliance Stack — automated risk assessment and proceed/halt/needs_human decision (Apr 27 at 9:05 AM)
**Investigated**: Pulumi preview output for `compliance-stack-OjinProd` was analyzed, covering all resource diffs, discovery diagnostics, and warning messages from the compliance stack run.

**Learned**: The OjinProd compliance stack manages ~161 resources across AWS CloudWatch alarms (ALB, SQS, DynamoDB, RDS, Lambda, EC2), GCP VPC Flow Logs, Cloudflare ZTNA (2 ECS Fargate connectors), SNS topics, and CloudWatch log retention enforcement. AWS discovery runs in cached mode (7-day TTL, currently 1.75 days old) — new resources in uncached regions may not appear until cache refresh. 18 VPCs are missing flow logs (pre-existing gap, not addressed by this plan). CW Log Retention is fully compliant (153/153).

**Completed**: Decision returned: `proceed`. The single planned change removes `filterExpr` from `gcp:networkmanagement:VpcFlowLogsConfig` for `ojin-production-default` — broadening flow log capture rather than narrowing it. 160 resources are unchanged. No destructive operations, no replacements, no IAM/security boundary changes.

**Next Steps**: Apply step likely follows — this was a preview analysis in a medium-risk workflow gate. No further investigation requested yet.


Access 764k tokens of past work via get_observations([IDs]) or mem-search skill.
</claude-mem-context>