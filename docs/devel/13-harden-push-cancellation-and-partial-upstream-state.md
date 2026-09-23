---
status: in-progress
issue: 13
branch-kind: fix
pr: null
completed: []
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

Retain separate stream readers but decouple reaping the direct process from output EOF: own the read/write pipe ends explicitly rather than calling `cmd.Wait` concurrently with `StdoutPipe`/`StderrPipe` readers (whose read ends `Wait` may close). Close the parent's write ends after Start, drain both read ends concurrently, and have exactly one reaper observe the direct child independently of descendants holding write ends. Coordinate context cancellation, direct-process completion and the kill in one lifecycle handshake. Do not rely on `exec.CommandContext`'s default `Cancel` watcher, `ctx.Err()` after `Wait`, `errors.Is(waitErr, context.Canceled)` or `ExitCode() == -1` as evidence that context killed Git: a signalled process also has exit code `-1`, and CommandContext can replace a successful wait with a context error after child exit. The handshake must not attribute a known completed direct process to a later cancellation, even if its pipes are still open; a successful `Kill` call alone is not proof of causation for a not-yet-reaped child. If simultaneous independent termination and cancellation cannot be causally distinguished, classify conservatively as the observed process outcome rather than claiming cancellation. Test the race where direct exit occurs before cancellation but before completion notification is consumed.

A cancellation before launch remains unstarted and cancelled with exit `-1`. When cancellation demonstrably terminates a live direct process, set `Cancelled` and preserve the reaped process status/error, joining the context cause so `errors.Is(Err(), context.Canceled)` is true. On independent failure, preserve the `*exec.ExitError` (including a signal exit), never join a late context error and keep `errors.As` working; join stream errors rather than replace process errors. Get `ExitCode` from reaped `ProcessState` even if the wait operation returns a context-wrapped error. After direct exit, inherited descriptors may remain open: retain all pre-close output and close read ends on context cancellation to bound that drain. A zero-exit child is not retroactively killed; if late closure produces a capture error, record it in `Err` without changing the existing `Success()` contract (`started && !cancelled && exitCode == 0`). The service still schedules success-only reconciliation; capture failure is not an upstream-refresh failure. Test that separation explicitly. A context that never cancels does not impose a new post-exit timeout; keep the existing EOF behavior in that case. Keep progress notification suppression and immutable result/popup contracts. Test ordered signal, nonzero, zero-exit, late-drain and live-context-kill cases with explicit synchronization and retained stream bytes; clean up descendants. Scope the signal fixture to supported platforms and keep portable cancellation tests.

### Branch upstream classification

On an attached, committed branch, probe both `branch.<captured-name>.remote` and `branch.<captured-name>.merge` through the generation-bound executor before resolving `@{upstream}` and counts. For each `git config --get`, only the verified missing-key result (exit 1 with empty stdout and stderr) is absence. Capture stdout and stderr separately: any stderr, start failure, other exit, or nonempty stdout on exit 1 is an error. A successful value is usable when trimmed stdout is one nonempty line; empty/whitespace-only or multiline success is an error, not absence. Do not restrict valid Git remote values (including `.`) or interpret merge-ref syntax here: syntactically supplied but unresolvable complete configuration still fails at the existing ref probe. Read both keys even when one is missing; errors win over absence, and errors for both keys are joined with both command identities for logs and reconciliation summaries. If either key is genuinely absent, return `UpstreamStateUnpublished` with empty upstream/count payload and a cleared icon via the existing atomic snapshot publication; neither rev-parse nor rev-list is needed. If both are configured, keep upstream-ref resolution and count errors as `UpstreamStateUnavailable` and preserve last-good identity/icon/counts. Return the actual failing config command for accurate logging. The same observation serves push preparation; valid remote-only state follows existing unpublished push behavior without changing its argv policy. Daemon remote/upstream reconciliation reports genuine read failures in the remote domain; successful absent reads clear its UI state. Detached and unborn branches retain their existing not-applicable classification.

### Failure, compatibility and rollback

No on-disk migration. Existing Git configuration stays untouched, including partial configuration; no new fetch or remote mutation is introduced. A successful push with an upstream read failure remains a successful push with a remote/upstream refresh warning. A revert of these code changes restores prior classification; any remote push already accepted by Git is not rolled back. Output stays in memory and Git argv is never reinterpreted through a shell.

## 3. Implementation

### Phase 1: Preserve direct push process outcomes across cancellation

**ID:** `1`
**Goal:** a push result distinguishes direct-process cancellation from independently terminated Git despite inherited output descriptors
**Primary invariant:** cancellation classification requires evidence that context affected the direct Git process, never just a late context error or signal-shaped exit code
**Commit point:** push execution and regression tests establish causal status, process error and captured streams without changing push arguments
**Dependencies:** none
**Allowed scope:** `api/git/commit.go`, `api/git/commit_test.go`, and narrowly required executor process plumbing/tests
**Exclusions:** upstream probing, UI redesign, daemon scheduling and push policy
**Counterexamples:** direct child signals itself while descendant holds stderr, then context expires; live Git killed by context; direct child exits zero before late context
**Tests:** pending

**Acceptance criteria:**

- [ ] A synchronized independently signal-terminated direct push with inherited descriptors and later context cancellation remains a non-cancelled process failure, with its real `Wait` error/status (`errors.As` to `*exec.ExitError`), retained stdout/stderr and no context-cancellation error; test exit before completion notification too.
- [ ] Cancellation that actually terminates a still-running direct push reports cancelled while retaining process status/error, context cause (`errors.Is`), and captured output.
- [ ] A direct process that completes successfully before late drain cancellation is not falsely marked killed and retains `Success() == true` and success-only reconciliation even if `Err` contains a stream capture failure. Independently nonzero exits are never relabelled cancelled; pre-start cancellation remains unstarted and cancelled.
- [ ] Concurrent draining, context-bounded shutdown of inherited descriptors, one reaper, immutable result accessors and existing push argv behavior remain intact.
- [ ] Focused regression tests use explicit ordering signals and do not leak descendant processes or depend on fragile wall-clock sleeps.

**Steps:**

1. Establish owned-pipe and direct-process reaping/cancellation handshake independent of output EOF; preserve `ProcessState` and bound inherited-pipe drain on cancellation.
2. Separate process causality, process failure and stream-read failure in result construction and logging, without changing success-only reconciliation eligibility.
3. Add ordered signal, genuine-cancellation and late-drain fixtures to `api/git/commit_test.go` (plus executor tests only if executor plumbing changes).

### Phase 2: Publish absent upstream for incomplete branch configuration

**ID:** `2`
**Goal:** refresh and post-push reconciliation classify partially configured attached branches without concealing real upstream errors
**Primary invariant:** either genuinely missing upstream key clears the complete remote/upstream snapshot; any actual read or configured-ref failure returns an error and keeps last-good values
**Commit point:** one atomic upstream classification path with refresh and reconciliation regression coverage
**Dependencies:** `1` (the push result must already preserve its direct outcome during reconciliation)
**Allowed scope:** `api/git/utils.go`, `api/git/remote.go`, `api/git/remote_test.go`, `api/git/commit_test.go`, `api/daemon_test.go`, and narrowly necessary test helpers
**Exclusions:** fetch policy, push refspec selection, detached/unborn semantics and UI layout
**Counterexamples:** remote-only, merge-only, failed config reads for either key, both keys set but upstream ref missing, and a post-push remote-domain failure
**Tests:** pending

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

<!-- Build fills this after implementation, including the full-sweep result. Keep it brief. -->
