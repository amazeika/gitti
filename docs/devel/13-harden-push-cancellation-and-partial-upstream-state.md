---
status: in-progress
issue: 13
branch-kind: fix
pr: 16
completed: [1.3, 2, 3, 4]
---

# Harden Push Cancellation and Partial Upstream State — Design Document

Issue [#13](https://github.com/amazeika/gitti/issues/13) follows the push-result and refresh work in #7. It corrects two Git outcome classifications: a direct push process that dies independently must not become a cancelled push because its context expires while descendants hold its output pipes, and a committed attached branch with only one upstream configuration key must publish a valid no-upstream snapshot. This is a targeted correctness fix, not a change to push policy or daemon scheduling.

## 1. Motivation

### Current state

`GitCommit.GitPush` (`api/git/commit.go`) concurrently drains stdout/stderr, waits for both readers or context cancellation, then calls `cmd.Wait()`. Its terminal switch prefers `ctx.Err()` over the actual wait error. A signal-terminated Git process with inherited pipe writers can therefore be reported as cancelled when the context expires after the direct process exited. `GitPushResult` then loses the direct error, although its streams were captured.

`resolveUpstreamObservation` (`api/git/utils.go`) uses `branchHasConfiguredUpstream`, which reads only `branch.<name>.remote`. With that key set and `branch.<name>.merge` missing, `@{upstream}` cannot resolve; the refresh in `api/git/remote.go` marks the observation unavailable and retains stale counts/identity rather than publishing the valid absent state. The same observation also feeds push preparation and post-push reconciliation.

### Goal

Show cancellation only when it caused termination of the direct push process, preserving independent process failure and captured output. Treat either missing upstream configuration key as a valid unpublished state while preserving last-good state and error-domain reporting for real probe failures. Do not change Git command selection, network behavior, detached/unborn handling, the existing refresh coordinator, or the semantics of fully configured but unresolvable upstreams.

### Use cases

- A user sees the actual signal failure, exit status and stdout/stderr after a direct Git process dies independently, even when cancellation later cuts off inherited pipe draining.
- A user cancelling a running push sees a cancelled result rather than a generic signal failure.
- A user removing only `branch.<name>.merge` sees the branch's upstream icon, identity and counts clear after refresh; a broken config read or configured ref probe instead surfaces an upstream failure without replacing last-good values.

## 2. Design

### Direct-process cancellation attribution

Retain separate stream readers but decouple reaping the direct process from output EOF: own the read/write pipe ends explicitly rather than calling `cmd.Wait` concurrently with `StdoutPipe`/`StderrPipe` readers (whose read ends `Wait` may close). Close the parent's write ends after Start, drain both read ends concurrently, and have exactly one reaper observe the direct child independently of descendants holding write ends. Coordinate context cancellation, direct-process completion and the kill in one lifecycle handshake. Do not rely on `exec.CommandContext`'s default `Cancel` watcher, `ctx.Err()` after `Wait`, `errors.Is(waitErr, context.Canceled)` or `ExitCode() == -1` as evidence that context killed Git: a signalled process also has exit code `-1`, and CommandContext can replace a successful wait with a context error after child exit. The handshake must not attribute a known completed direct process to a later cancellation, even if its pipes are still open. A successful `Kill` call alone is not proof of causation for a not-yet-reaped child. When the reaped status disagrees with the cancellation signal (an independent SIGKILL, any other signal, or any numeric exit), classify conservatively as the observed process outcome rather than claiming cancellation. The one indistinguishable overlap, an independent SIGTERM racing the handshake's own SIGTERM with no known earlier completion and a matching reaped SIGTERM status, keeps the cancellation claim under the accepted phase-1.3 policy without asserting which signal caused death. Test the race where direct exit occurs before cancellation but before completion notification is consumed.

A cancellation before launch remains unstarted and cancelled with exit `-1`. When cancellation demonstrably terminates a live direct process, set `Cancelled` and preserve the reaped process status/error, joining the context cause so `errors.Is(Err(), context.Canceled)` is true. On independent failure, preserve the `*exec.ExitError` (including a signal exit), never join a late context error and keep `errors.As` working; join stream errors rather than replace process errors. Get `ExitCode` from reaped `ProcessState` even if the wait operation returns a context-wrapped error. After direct exit, inherited descriptors may remain open: retain bytes read before closure and close read ends on context cancellation to bound that drain. Pipe bytes still unread when a reader stays blocked past the timed cutoff may be lost. A zero-exit child is not retroactively killed; if late closure produces a capture error, record it in `Err` without changing the existing `Success()` contract (`started && !cancelled && exitCode == 0`). The service still schedules success-only reconciliation; capture failure is not an upstream-refresh failure. Test that separation explicitly. A context that never cancels does not impose a new post-exit timeout; keep the existing EOF behavior in that case. Keep progress notification suppression and immutable result/popup contracts. Test ordered signal, nonzero, zero-exit, late-drain and live-context-kill cases with explicit synchronization and retained stream bytes; clean up descendants. Scope the signal fixture to supported platforms and keep portable cancellation tests.

### Branch upstream classification

On an attached, committed branch, probe both `branch.<captured-name>.remote` and `branch.<captured-name>.merge` through the generation-bound executor before resolving `@{upstream}` and counts. For each `git config --get`, only the verified missing-key result (exit 1 with empty stdout and stderr) is absence. Capture stdout and stderr separately: any stderr, start failure, other exit, or nonempty stdout on exit 1 is an error. A successful value is usable when trimmed stdout is one nonempty line; empty/whitespace-only or multiline success is an error, not absence. Do not restrict valid Git remote values (including `.`) or interpret merge-ref syntax here: syntactically supplied but unresolvable complete configuration still fails at the existing ref probe. Read both keys even when one is missing; errors win over absence, and errors for both keys are joined with both command identities for logs and reconciliation summaries. If either key is genuinely absent, return `UpstreamStateUnpublished` with empty upstream/count payload and a cleared icon via the existing atomic snapshot publication; neither rev-parse nor rev-list is needed. If both are configured, keep upstream-ref resolution and count errors as `UpstreamStateUnavailable` and preserve last-good identity/icon/counts. Return the actual failing config command for accurate logging. The same observation serves push preparation; valid remote-only state follows existing unpublished push behavior without changing its argv policy. Daemon remote/upstream reconciliation reports genuine read failures in the remote domain; successful absent reads clear its UI state. Detached and unborn branches retain their existing not-applicable classification.

### Failure, compatibility and rollback

No on-disk migration. Existing Git configuration stays untouched, including partial configuration; no new fetch or remote mutation is introduced. A successful push with an upstream read failure remains a successful push with a remote/upstream refresh warning. A revert of these code changes restores prior classification; any remote push already accepted by Git is not rolled back. Output stays in memory and Git argv is never reinterpreted through a shell.

## 3. Implementation

### Phase 1.3: Preserve causal direct-push outcomes and owned-pipe cleanup
**Goal:** Deliver one green direct-push lifecycle with user-cancellation outcome classification, retained process status and streams, bounded inherited-descriptor draining, and owned-pipe cleanup; retain the current candidate as the starting point.
**Primary invariant:** Without a cancellation request, SIGTERM is an observed process failure, not a cancellation. After a cancellation request, when the direct process was not already known complete, cancellation SIGTERM was delivered successfully and the reaped status agrees with SIGTERM, report cancelled even if an independent simultaneous SIGTERM is indistinguishable. Do not claim proof of signal provenance. Known completed processes and nonmatching failures keep their observed outcomes. Owned read ends close on ordinary and cancelled returns.
**Commit point:** One independently reviewable green commit of the causal lifecycle, ordered regression evidence, cross-file contract and all inherited tests; only then can Phase 2 depend on completed lineage root `1`.
**Dependencies:** none; test repair and pre-coding red checks are workflow steps, not acceptance gates.
**Acceptance criteria:**
- [ ] A synchronized independently signal-terminated direct push with inherited descriptors and later context cancellation remains a non-cancelled process failure, with its real `Wait` error/status (`errors.As` to `*exec.ExitError`), retained stdout/stderr and no context-cancellation error; test exit before completion notification too.
- [ ] A cancellation requested before direct-process completion, with successful delivery of cancellation SIGTERM and matching reaped SIGTERM status, reports cancelled with retained process status/error, context cause (`errors.Is`), and captured output. A final-gap independent same-SIGTERM is not required to be distinguished from the cancellation signal; without a cancellation request, SIGTERM reports the proper process failure without a context-cancellation error.
- [ ] A direct process that completes successfully before late drain cancellation is not falsely marked killed and retains `Success() == true` and success-only reconciliation even if `Err` contains a stream capture failure. Independently completed nonzero exits and exits with a nonmatching status are not relabelled cancelled; pre-start cancellation remains unstarted and cancelled.
- [ ] Concurrent draining, context-bounded shutdown of inherited descriptors, one reaper, immutable result accessors and existing push argv behavior remain intact.
- [ ] Focused regression tests use explicit ordering signals and do not leak descendant processes or depend on fragile wall-clock sleeps.
- [ ] The independently authored additive tests and unchanged frozen `api/git/commit_test.go` pass at the final green commit point; any observed red signal is resolved before this phase is accepted.
- [ ] R3: a completed ps diagnostic nonzero exit with stderr, or ps terminated by signal, is unknown liveness rather than positive child death; cancellation of a still-live direct child does not hang or skip termination. Only verified dead/zombie evidence suppresses a kill, while ambiguous independent SIGKILL remains a non-cancelled observed outcome.
- [ ] R4: ordinary successful or failed push completion closes both owned pipe read ends without relying on garbage collection; cancellation still bounds inherited-descriptor draining and retains bytes read before closure.
- [ ] R5: a context cancelled during ActiveGuard or setup before Start produces an unstarted cancelled result with exit -1, closes all pipe ends and launches no push.
- [ ] Before acceptance, verify the phase document, commit.go and Unix probe agree on the accepted cancellation-outcome policy: an active cancellation with successful SIGTERM delivery, no known earlier completion and matching reaped SIGTERM is cancelled despite an indistinguishable independent same-SIGTERM race. No cancellation request means SIGTERM is a process failure. Do not require a final-gap same-signal differentiation oracle or claim that matching status proves signal provenance; retain ordered independent-before-cancel, distinguishable-exit and live-cancel tests.
- [ ] Repair only the four explicitly authorized frozen cross-file policy-pin test paths declared below, minimally replacing old conservative-contract source-text assertions with checks for the accepted policy while preserving their behavioral tests. Resolve any other invalid frozen fixture through a separate explicit test-repair decision; no other frozen test is quietly edited or treated as green by a sibling test.
**Steps:**
1. Test-repair cluster first: inspect the frozen volume-drain fixture and any observed portability/red fixture against the checkpoint. The current volume test orders descendant start, volume completion and both readers' progress before cancellation; verify that ordering and teardown in the frozen bytes rather than assuming the older underordered snapshot is current. If any frozen fixture still needs repair, request an explicit frozen-test exception with the exact file and minimal test-only diff, baseline it against unchanged production, and do not edit it until authorized; otherwise keep it byte-for-byte. Do not seek an oracle to distinguish two indistinguishable final-gap SIGTERMs. Keep ordered independent SIGTERM before cancellation, distinguishable SIGKILL/numeric-exit and genuine live-cancel behavioral coverage; minimally repair only the four explicitly authorized frozen cross-file policy pins to reflect this amendment, without weakening those behavioral oracles. Run pre-coding baseline/red checks as evidence only.
2. Cancellation/completion cluster (owner: `api/git/commit.go`, with narrowly required platform helper changes only): compare actual Wait-channel ordering, final observation-to-signal window and reaped status with the amended policy. Retain the one-reaper owned-pipe implementation, pre-Start guard cleanup, read-before-close cutoff and candidate work. A delivered cancellation SIGTERM with matching reaped SIGTERM after a request and no known completed Wait may be classified cancelled even if an independent SIGTERM in the final gap is indistinguishable. Do not misstate this policy as causal proof. Do not require a final-gap same-signal oracle. Do not use longer polling to claim provenance. Preserve known completion before cancellation, no-request SIGTERM failure, failed delivery, independently observed nonmatching exits and genuine live cancellation. Own the commit.go completed-Wait versus simultaneous-overlap explanation here, not in the Unix-comment cluster; document the accepted policy rather than treating the overlap as a blocker.
3. Unix liveness prose cluster (owner: `api/git/push_liveness_unix.go` only): retain the current comment-only edits. Verify the three passages against kernel ESRCH, /proc or Darwin zombie and live evidence, unknown EPERM/ps failures (including signalled ps and nonzero stderr), the silent exit-1 exception, the bounded diagnostic-window poll and the final signal gap. Do not assign commit.go or the document to this cluster; rerun its relevant comment/diff, build and focused-test checks after any authorized edit.
4. Phase prose cluster (owner: `docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md` only): split step 2 into liveness/reaping, classification and status/cause actions; separate counterexamples into short independent cases. Keep all recorded criteria, R4 read-before-close limit and the frozen-fixture status accurate to the current bytes. State the accepted same-SIGTERM cancellation-outcome policy explicitly without claiming proof of which signal caused death; align the design goal and direct-process cancellation attribution section with this decision. Rerun document verification after its own edit. The commit.go completion prose stays with the causal/completion owner; rerun that owner's verification after its edit.
5. Cross-file review the document, commit.go and Unix helper together. Exercise ordered independent signal, exit-before-notification, actual live cancellation, ps diagnostic failure, late success/reconciliation, guard-time cancellation, both ordinary EOF closures and inherited-writer cutoff; preserve real `Wait`/`ProcessState`, `errors.As`/`errors.Is`, both streams, immutable accessors and argv. Run supported-platform compilation, focused regressions, all frozen tests and phase lint/review to green only after the four authorized policy-pin test repairs and amended cancellation-policy checks. Roll back only this process-path candidate on failure, never an accepted remote push. A passing gate without preserved ordered pre-cancel and live-cancel evidence, cross-file policy agreement or frozen-test resolution is not this commit point.
**Counterexamples:**
- A failed ps diagnostic suppresses termination of a live child, or a zombie's successful signal is called causal.
- Independent SIGKILL or numeric exit is relabelled as cancelled.
- A SIGTERM without a cancellation request is logged as cancelled.
- An ambiguous same-SIGTERM overlap during an active cancellation is described as proven signal causation instead of the accepted cancellation-outcome policy.
- Completion before notification, zero/nonzero exit or late drain cancellation is relabelled; a live context termination loses status, cause or streams.
- Guard cancellation starts Git; ordinary EOF leaves read ends open; timed closure loses already-read bytes, hangs on an inherited writer or leaks descendants; push argv changes.
- A frozen fixture is edited without authorization, the volume prerequisite gap is hidden by a sibling test, or cross-file prose contradicts executable behavior.
**Tests:** `api/git/commit_cancellation_regression_test.go`, `api/git/commit_kill_causality_test.go`, `api/git/commit_test.go`, `api/git/push_cancellation_causality_additive_test.go`, `api/git/push_cancellation_r5_setup_cleanup_test.go`, `api/git/push_cancellation_revision_test.go`, `api/git/push_causality_probe_remediation_test.go`, `api/git/push_custom_cancel_cause_additive_test.go`, `api/git/push_final_boundary_remediation_test.go`, `api/git/push_phase111_causal_lifecycle_test.go`, `api/git/push_phase111_close_cutoff_additive_test.go`, `api/git/push_phase111_independent_additive_test.go`, `api/git/push_phase111_ordered_boundary_additive_test.go`, `api/git/push_phase13_causal_cleanup_additive_test.go`, `api/git/push_phase13_causal_outcome_additive_test.go`, `api/git/push_phase13_missing_procfs_regression_test.go`, `api/git/push_phase13_ordered_probe_gap_regression_test.go`, `api/git/push_phase13_preserve_owned_cleanup_additive_test.go`, `api/git/push_phase13_same_sigterm_gap_additive_test.go`, `api/git/push_probe_kill_attribution_additive_test.go`, `api/git/push_process_causality_round1_test.go`, `api/git/push_queued_output_remediation_test.go`, `api/git/push_same_signal_causality_additive_test.go`, `api/git/push_volume_ordering_additive_test.go`, `api/git/push_phase13_policy_outcome_additive_test.go`
**Exclusions:** upstream probing, UI redesign, daemon scheduling and push policy
**Allowed scope:** `api/git/commit.go` for causal/completion work; `api/git/push_liveness_unix.go` for Unix-comment ownership and only evidence-required probe changes; narrowly necessary existing portable process-probe plumbing; `docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md` for phase prose and alignment of its design goal and direct-process attribution text; existing additive tests for ordered pre-cancel and live-cancel evidence, with no final-gap same-SIGTERM distinction required. Inherited test paths remain frozen and read-only except the four minimal policy-pin repairs explicitly authorized in this amendment: `api/git/push_phase13_causal_cleanup_additive_test.go`, `api/git/push_same_signal_causality_additive_test.go`, `api/git/push_phase13_same_sigterm_gap_additive_test.go`, and `api/git/push_phase13_ordered_probe_gap_regression_test.go`. Other frozen test repairs require separate explicit authorization before mutation. No Phase 2, Full test sweep, Outcome or Documentation work in this replacement.

### Phase 2: Publish absent upstream for incomplete branch configuration

**ID:** `2`
**Goal:** refresh and post-push reconciliation classify partially configured attached branches without concealing real upstream errors
**Primary invariant:** either genuinely missing upstream key clears the complete remote/upstream snapshot; any actual read or configured-ref failure returns an error and keeps last-good values
**Commit point:** one atomic upstream classification path with refresh and reconciliation regression coverage
**Dependencies:** `1` (the push result must already preserve its direct outcome during reconciliation)
**Allowed scope:** `api/git/utils.go`, `api/git/remote.go`, `api/git/remote_test.go`, `api/git/commit_test.go`, `api/daemon_test.go`, and narrowly necessary test helpers
**Exclusions:** fetch policy, push refspec selection, detached/unborn semantics and UI layout
**Counterexamples:** remote-only, merge-only, failed config reads for either key, both keys set but upstream ref missing, and a post-push remote-domain failure
**Tests:** `api/git/phase2_absent_upstream_test.go`, `api/phase2_postpush_refresh_test.go`

**Acceptance criteria:**

- [ ] On an existing attached branch with only remote configured, refresh succeeds as unpublished and atomically clears upstream identity, icon to its no-upstream default, and ahead/behind counts; the merge-only and neither-key variants behave likewise.
- [ ] For both config keys, tests distinguish verified absence from start failure, exit 1 with stdout/stderr, other exits, zero-exit stderr, whitespace-only/multiline success, and usable single-line values (including `.`); failures win over another key's absence and identify each failing command.
- [ ] With both keys set, a failed upstream ref/count probe reports an error, sets unavailable health and retains the last-good snapshot, including icon and counts.
- [ ] A partial-key post-push ticket is refreshed with empty failed domains and the default no-upstream icon; a genuine complete-config probe failure names `remote/upstream`, keeps last-good, and leaves a successful push distinct from its refresh failure.
- [ ] Neither upstream-ref resolution nor counts run for valid partial configuration. Remote-only, merge-only and neither-key branches use existing unpublished push arguments without refusal; config-read failure on either key refuses before process start. Tracked, detached and unborn behavior remains intact.

**Steps:**

1. Probe and classify both branch-scoped config keys through the pinned executor, preserving the failing command and separating missing keys from command errors.
2. Reuse the existing snapshot publication behavior for unpublished and unavailable states; verify icon and counts together, not only classification.
3. Add real-repository partial-config transitions and injected command failures for each key, including a failure paired with the other key's absence; exercise complete-config ref and count failure and the post-push remote-domain error path.

### Phase 3: Full test sweep

**ID:** `3`
**Goal:** every test declared by this spec is green together
**Tests:** all

**Acceptance criteria:**

- [ ] The union of test paths declared by completed phases passes through the scoped resolver.
- [ ] Failures surfaced by the sweep are remediated in this phase, with their remediation tests added after `all` if authored.

### Phase 4: Outcome

**ID:** `4`
**Goal:** reconcile delivered behavior, deviations, decisions, deferred work and full-sweep evidence into `Outcome`
**Tests:** none — this phase records evidence and authors no tests

**Acceptance criteria:**

- [ ] Open Questions are resolved or explicitly deferred before Outcome and the result records the Full test sweep.

### Phase 5: Documentation

**ID:** `5`
**Goal:** document shipped cancellation and upstream classification with verification instructions
**Tests:** none — documentation work authors no automated tests

**Acceptance criteria:**

- [ ] Invoke `$ckit:docs` through its build-owned route; reconcile existing docs or use the nearest relevant README, falling back to root README.
- [ ] Explain independent signal failure versus cancellation and valid no-upstream versus genuine refresh failure.

## 4. Verification

- [ ] Focused regressions for both phases pass, followed by `go test ./...` and `go test -race ./api ./api/git`.
- [ ] A remote-only branch refresh clears identity/icon/counts, while an injected complete-config ref failure preserves them and post-push reconciliation reports the remote/upstream domain.

## 5. Open Questions

No unresolved design questions; implementation may choose the synchronization primitive so long as direct-process causality is demonstrably tested.

## 6. Advisory Sweep

| Track | Status | Configured model | Actual model | Effort | Verdict | Evidence and disposition |
| --- | --- | --- | --- | --- | --- | --- |
| `astra-high` | ran | `gpt-6-astra` | `gpt-6-astra` | high | needs-attention | Confirmed both reported defects; required one reaper with owned pipes, direct-process cancellation causality, explicit late-drain success/error behavior, and precise per-key config validation. Incorporated into design and phase criteria. |
| `grok-high` | ran | `grok-4.7` | `grok-4.7` | high | needs-attention | Identified `exec.CommandContext`'s late-cancel wait rewrite, `ProcessState` versus `Wait` error, and remote-only push-preparation consequences. Incorporated causal handshake, result retention, valid-value contract and push/reconciliation oracles. |

Both configured tracks returned schema-valid advice at configured models/efforts; no coverage gap. No tests were executed during the advisory sweep.

## Outcome


Phase 1.3 (`a59d39f`) delivered a single direct-process reaper with owned pipes: independent failures retain process status, errors and captured output rather than becoming late cancellations; live cancellation retains the context cause, and inherited-writer draining is bounded when the context cancels. An indistinguishable concurrent SIGTERM is classified as cancelled under the documented policy, without claiming signal provenance. Phase 2 (`8ac6562`) probes both upstream keys: verified absence of either publishes an empty upstream/count snapshot and default icon; actual config or ref/count failures retain last-good state and surface a remote/upstream error separately from push success. Phase 3 (`2cbb574`) recorded a passed full sweep: `go test ./...` (package results reported as cached) and `go build -o ./bin/gitti .`; no scope drift or diagnostics were recorded. The sweep does not establish the separate `go test -race ./api ./api/git` verification listed above. All three phase closes record no deviations, additional decisions, open-question changes or scope changes. No product questions remain unresolved; the documentation phase remains planned.

