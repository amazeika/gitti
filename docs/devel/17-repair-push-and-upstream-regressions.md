---
status: in-progress
issue: 17
branch-kind: fix
pr: 18
completed: [1, 2]
follow-up:
  issue-url: https://github.com/amazeika/gitti/issues/17
  source-issue-url: https://github.com/amazeika/gitti/issues/13
  source-pr-url: https://github.com/amazeika/gitti/pull/16
  source-spec-url: https://github.com/amazeika/gitti/blob/d6f2a86e21904798ed878069bb9f35dbfe7806d2/docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md
  base-branch: null
  base-commit: null
---

# Repair Push Outcomes and Upstream Observations — Design Document

Issue [#17](https://github.com/amazeika/gitti/issues/17) follows merged [#16](https://github.com/amazeika/gitti/pull/16) and its [committed specification](https://github.com/amazeika/gitti/blob/d6f2a86e21904798ed878069bb9f35dbfe7806d2/docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md). It closes the recorded push-outcome and upstream-observation defects and test gaps, then makes the corresponding contracts readable. It does not add new push features or change the accepted same-SIGTERM ambiguity policy.

## 1. Motivation

### Current state

`api/git/commit.go` implements the accepted cancellation request/delivery/reaped-status policy but its terminal comment still calls cancellation demonstrably causal. Two inherited-descriptor tests in `push_phase13_policy_outcome_additive_test.go` cancel before acknowledging holder startup. `commit_test.go` calls a late-cancel scenario “before completion notification” without proving the independent reaper's buffered `waitCh` has not already been consumed. The source spec and Unix helper comments contain the recorded dense or conflicting explanations.

`api/git/remote.go` stores a last *remote-tracking* payload separate from the latest successful published generation. After an unpublished or local-dot publication, a failed read combines an older remote payload with the newest branch label. In `api/git/utils.go`, local-dot handling resolves the last `git config --get branch.<name>.merge` value directly; Git's `branch@{upstream}` uses the first merge value, so the shortcut can publish a fictitious tracked upstream and allow push preparation. The existing config start-failure test removes Git from PATH entirely; it never reaches either per-key config command.

### Goal

After a failed upstream read, expose unavailable health with the *latest successfully published* branch and payload intact. Match Git's upstream resolution for local-dot remotes, including multiple merge values; fail safely when Git cannot resolve one. Make cancellation evidence and documentation agree with the accepted policy, and pin the two missing timing and config-start error cases with ordered tests. Preserve existing UI and push behavior for valid tracked, unpublished, detached and unborn states.

### Use cases

- A user whose branch becomes unpublished or tracks a local ref sees that state retained when a later refresh fails, rather than an unrelated old remote icon/count.
- A user with conflicting local-dot merge values never sees a tracked ref that Git cannot resolve, and push preparation does not run on an invented upstream.
- A maintainer can distinguish an acknowledged inherited writer, a direct child that exited before an *unconsumed* notification, and a config command that failed to start from a fixture that merely passed.

## 2. Design

### Cancellation outcome and evidence

Keep the process handshake, one reaper, owned pipes and accepted rule from #13: a requested cancellation with successful SIGTERM delivery, no previously known completion, and matching reaped SIGTERM status reports cancelled. The matching status does **not** prove which simultaneous SIGTERM caused death. A completed child or a different reaped status retains its observed outcome. Change the terminal comment and source document Goal to describe this outcome policy rather than an impossible causality claim; shorten the documented attribution, cross-file instruction and frozen-test scope without changing source-spec history or the shipped status.

The two holder fixtures must wait for their existing `STARTED` marker *and* the pre-cancel output before cancelling, then release/reap holders on failure as on success. The notification regression must gate the handshake at a deterministic point after direct `Wait` returns but before the result consumes its completion notification. Assert the gate is reached before cancelling and release it on all paths; retain descendant-held descriptors, captured streams, and independent-process-error assertions. Introduce a narrowly scoped test seam only if a test-only synchronization mechanism cannot prove that ordering without changing production outcomes. Do not infer notification ordering from pipe EOF, ps observations or elapsed sleeps.

### Snapshot and local-dot resolution

A successful atomic publication is the sole payload baseline, including unpublished and local-dot observations. On a failed current-generation read, retain the current published payload and observed branch, set only health to `UpstreamStateUnavailable`, and return the read error. Repeated failures keep that payload; a stale `PublishGuard` prevents *any* publication. Avoid concurrent success/error stores mixing two generations: follow the existing serialized refresh-coordinator contract or synchronize the read/guard/publication boundary as needed, and test a rejected stale generation. Remove the obsolete remote-only restoration cache if it has no other consumer.

Keep probing *both* branch-scoped config keys: verified absence is unpublished, any read/start error wins over absence. With complete config and `remote=.`, use Git's captured-branch `@{upstream}` resolution and count path, as for ordinary remotes, rather than assuming the last `branch.merge` value is authoritative. Preserve the local ref's identity/icon and counts on valid single-merge configuration. With multiple merge values, compare the published upstream and counts to Git's own `branch@{upstream}`: test both a resolvable first ref followed by a different resolvable ref (use the first), and an unresolvable first ref followed by a resolvable ref (publish unavailable health with the latest successful payload, return an error, and refuse push preparation before starting Git push). Test config start failures **per key** through `readUpstreamConfigKey` or a selected-command executor failure; assert the failing command's identity and distinction from missing key while the other key remains readable.

### Compatibility, failure and rollback

No Git config rewrite, disk migration, remote mutation, or network policy change. Existing successful pushes are never undone by failed refresh; a post-push upstream error remains separate from push success. Keep current remote inventory and fetch behavior. Revert this follow-up's code changes to restore prior classification, not to roll back a completed remote push. Failures publish unavailable health with an error for the remote/upstream domain; neither parse nor start failures become a false unpublished state.

### Finding dispositions

Stable identifiers below refer to the unchecked findings in #17 in their issue order. Every finding is **actionable** against the merged code at `faa4bbf` (source lines may have moved); the named phase owns its acceptance check. No finding is silently closed by a passing prior test run.

| Finding | Source anchor / disposition | Owner |
| --- | --- | --- |
| F01 | `commit.go:1004` causal-only terminal comment; align with same-SIGTERM outcome policy | 1 |
| F02 | `push_phase13_policy_outcome_additive_test.go:667` both holder-start races | 1 |
| F03 | source spec:35 dense direct-process attribution | 1 |
| F04 | source spec:64 cross-file verification sentence | 1 |
| F05 | source spec:67 frozen-test instruction | 1 |
| F06 | source spec:82 allowed-scope sentence | 1 |
| F07 | `push_liveness_unix.go:185` signal/zombie comment | 1 |
| F08 | `push_liveness_unix.go:201` status/escalation comment | 1 |
| F09 | `remote.go:579` restores stale remote-only snapshot | 2 |
| F10 | `phase2_absent_upstream_test.go:329` missing per-key start-failure oracles | 2 |
| F11 | `daemon_partial_upstream_test.go:20` multi-purpose fake contract comment | 2 |
| F12 | `remote.go:49` remote-only last-good comment | 2 |
| F13 | `remote.go:556` unavailable-publication comment | 2 |
| F14 | `utils.go:379` config-value validation comment | 2 |
| F15 | `utils.go:436` local-dot comment | 2 |
| F16 | `upstream_partial_config_test.go:233` validation-matrix comment | 2 |
| F17 | `upstream_partial_config_test.go:352` error precedence comment | 2 |
| F18 | `upstream_partial_config_test.go:537` push-argv comment | 2 |
| F19 | `utils.go:318–319` last merge value differs from Git's first; real-config oracle | 2 |
| F20 | `commit_test.go:1381` completion notification not demonstrably pending | 1 |

## 3. Implementation

### Phase 1: Reconcile push cancellation policy and ordered fixtures

**ID:** `1`
**Goal:** retain the existing accepted push outcome while making its race evidence and user-facing contract accurate.
**Primary invariant:** a known completed direct process keeps its failure even if cancellation arrives before an unconsumed completion notification; an active request with delivered matching SIGTERM follows the accepted cancellation-outcome rule without claiming signal provenance.
**Commit point:** one green commit with synchronized process/holder regressions and aligned policy explanations.
**Dependencies:** none
**Allowed scope:** `api/git/commit.go`, `api/git/commit_test.go`, `api/git/push_phase13_policy_outcome_additive_test.go`, `api/git/push_liveness_unix.go`, `docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md`; a narrowly necessary push test seam only if deterministic ordering cannot be established test-only.
**Exclusions:** upstream classification, push argv, signal-policy changes, deleting behavioral oracles, altering the shipped source spec's frontmatter or completed phase records.
**Counterexamples:** cancellation before a holder has started; a test passing after the reaper notification was already consumed; source Goal claiming all cancelled outcomes were caused by our SIGTERM; a frozen test loosened rather than strengthened.
**Tests:** `api/git/push_phase1_ordered_policy_test.go`

**Acceptance criteria:**

- [ ] F02: both inherited-descriptor fixtures acknowledge their own holder `STARTED` markers before cancel; they still check pre-cancel streams, bound draining and release/reap descendants, including on failure.
- [ ] F20: the direct child exits before cancellation *and* the completion notification remains unconsumed at cancellation by explicit synchronization; its result is not cancelled and retains `*exec.ExitError`, status, both streams and no context-cancellation error. The fixture fails if the synchronization prerequisite is not established.
- [ ] F01, F03–F08: source spec Goal and attribution, cross-file verification/frozen-test/scope wording, `commit.go` terminal contract, and Unix signal/status comments describe the accepted matching-SIGTERM outcome and are split into short, nonconflicting claims. The source spec remains shipped and historical acceptance criteria are not reinterpreted as new features.
- [ ] Existing no-request signal failure, known completion, distinguishable-exit, genuine live cancellation, ordinary EOF and guard-time cancellation regressions remain green; no process leak or new production synchronization stall.

**Steps:**

1. Baseline both fixtures and identify an explicit gate between direct `Wait` completion and handshake receipt; author/repair test oracles and holder-start prerequisites without substituting timers for ordering.
2. Align the comments and shipped spec's prose with the accepted policy; preserve its historical phase/test record and test-only authorization for the two named files.
3. Exercise focused signal, notification, descriptor, live-cancel and cleanup tests, then check document/code consistency and run the phase gates. A gate failure is phase work, not a completed phase.

### Phase 2: Preserve successful upstream snapshots and Git-resolved local tracking

**ID:** `2`
**Goal:** one coherent upstream observation path retains the latest payload on failure and agrees with Git for local-dot refs and per-key read failures.
**Primary invariant:** each current-generation success publishes one complete observation; a failed read changes only health and reports the error, while local-dot tracking is never fabricated from the wrong merge value.
**Commit point:** one green commit with real-config local-dot, successful-state-to-error, per-key start-failure and stale-guard regressions.
**Dependencies:** `1` (separate push-outcome evidence is green before testing push preparation and reconciliation against upstream state).
**Allowed scope:** `api/git/remote.go`, `api/git/utils.go`, `api/git/phase2_absent_upstream_test.go`, `api/git/upstream_partial_config_test.go`, `api/daemon_partial_upstream_test.go`, and narrowly necessary `api/git` snapshot/push-preparation tests or refresh synchronization.
**Exclusions:** fetch policy, remote inventory, refspec redesign, changing partial-key/attached/detached/unborn semantics, replacing valid local-dot upstreams with unpublished state.
**Counterexamples:** tracked remote → unpublished → read failure restoring old icon; tracked remote → local-dot → failure restoring old remote; multiple merge values with first unresolvable and last resolvable being marked tracked, or first resolvable and last different being resolved to the last; removing Git from PATH before the config command is selected; guard rejection publishing unavailable health.
**Tests:** `api/daemon_partial_upstream_test.go`, `api/git/phase2_repair_snapshot_localdot_test.go`, `api/git/upstream_partial_config_test.go`, `api/git/phase2_trailing_newline_first_merge_test.go`

**Acceptance criteria:**

- [ ] F09: after successful tracked, unpublished, and local-dot publications, a failing config/ref/count read reports unavailable and retains the latest full published branch, identity, icon and counts (including cleared values); repeated errors do not resurrect an older remote snapshot. A stale guard leaves state unchanged.
- [ ] F19: valid single-merge `remote=.` resolves and counts the captured branch's actual Git upstream. In a real repository with two different resolvable merge refs, identity and counts agree with Git's first-ref `branch@{upstream}` rather than `git config --get`'s last value. With first merge ref missing and later merge ref resolvable, observation fails unavailable, keeps latest successful payload, and push preparation refuses before starting a push; no invented tracked ref.
- [ ] F10: independent unstartable config-command cases for `remote` and `merge` identify the failing key, return errors instead of absence even if the other key is missing, and preserve the latest successful snapshot; the existing broad PATH failure test alone is not sufficient.
- [ ] F11–F18: comments in daemon fake, remote snapshot/publication, config validation, local-dot resolution and test contracts distinguish read behavior, errors, value rules, and retained payload concisely and accurately.
- [ ] Tracked remote, single-merge local-dot, partial-key unpublished, detached/unborn, post-push success with a separate remote/upstream failure, and generation-bound publication behavior remain green.

**Steps:**

1. Author real-config transitions, both resolvable and unresolvable-first multi-merge regressions, and per-key start-failure regressions; demonstrate the old misclassifications and retain an independent success/error oracle and a guard-rejection oracle.
2. Make failure publication retain the latest complete payload, and make local-dot resolve through Git's captured-branch upstream and counts; remove remote-only cache and obsolete shortcut if unused.
3. Reconcile the named comments with the corrected contract; run phase tests and lint/review with refresh, push-preparation and daemon-domain coverage before marking the commit point green.

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
**Goal:** reconcile shipped behavior and verification instructions in the repository's established documentation system
**Tests:** none — documentation work authors no automated tests

**Acceptance criteria:**

- [ ] The Build-owned Documentation phase reconciles the repository documentation owner, or the nearest relevant README; if neither exists, create the first concise page above `docs/devel/`.
- [ ] Explain accepted push cancellation outcome and last-successful upstream payload/error behavior without asserting impossible signal provenance.

## 4. Verification

- [ ] Focused affected tests, `go test -count=1 ./...`, `go test -race -count=1 ./api ./api/git`, `go vet ./...` and application build pass on the supported host.
- [ ] Refresh and push-preparation failures name the upstream error domain, while completed pushes remain completed; ordered cancellation regressions distinguish cause-independent completion from the accepted same-signal outcome.

## 5. Open Questions

None. The same-SIGTERM overlap is an explicitly accepted outcome policy, not a request for a signal-provenance oracle.

## 6. Advisory Sweep

| Track | Status | Configured model | Actual model | Effort | Verdict | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| `astra-high` | ran | `gpt-6-astra` | `gpt-6-astra` | high | sound | Confirmed snapshot generation, per-key config and independent reaper boundaries; no blocking challenge. |
| `grok-high` | ran | `grok-4.7` | `grok-4.7` | high | sound | Confirmed matching-SIGTERM policy, frozen-test scope, local-dot failure and push-preparation refusal; no blocking challenge. |

Both configured lanes returned schema-valid advice on this draft. No tests were run during the advisory sweep.

## Outcome

<!-- Build fills this after implementation, including the full-sweep result. Keep it brief. -->
