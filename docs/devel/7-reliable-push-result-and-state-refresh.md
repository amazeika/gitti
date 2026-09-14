---
status: in-progress
issue: 7
pr: null
completed: [1]
---

# Reliable Push Result and State Refresh — Design Document

Issue [#7](https://github.com/amazeika/gitti/issues/7) reports that a successful normal push from a tracked branch can leave the Commit Log remote marker and the ahead/behind counters stale until a later force push causes another refresh. This change gives every push a definitive, inspectable process result and causally schedules a no-drop refresh of branch, upstream, remote-tracking, and Commit Log state after success, including in linked worktrees. It does not change Git's push policy or make force pushing part of recovery.

## 1. Motivation

### Current state

`GitCommit.GitPush` (`api/git/commit.go`) checks the current upstream, builds one of the normal, safe-force, or force push commands, combines stderr into stdout, streams normalized lines into `gitRemotePushOutput`, and returns only an integer status. The service (`tui/services/push.go`) reduces that status to `Success bool`; the popup then shows a green or red border. The command is present in the separate log panel, but the push popup does not identify the executed working directory or argv, does not show an explicit exit status, and cannot distinguish stdout from stderr. Lock refusal and pipe/start failures all collapse to `-1`, which can leave an empty red popup with no actionable explanation.

A successful push has no causal refresh path. The UI currently relies on filesystem events under the common Git directory to call `GitDaemon.gitLatestInfoFetch`. Each refresh domain uses `CompareAndSwap(false, true)` and silently skips a request when its worker is already active. Therefore an event caused by the push can be coalesced away without guaranteeing a pass that begins after the push. Ahead/behind state may eventually be repaired by the periodic remote timer, but Commit Log decorations have no equivalent periodic guarantee. A second push creates another chance for the watcher, explaining why it can appear to repair the first operation without proving that force was required.

The executor already sets `exec.Cmd.Dir` to the selected repository/worktree. In a linked worktree the watched common Git directory and the command working directory intentionally differ, but the completed push result does not expose the latter, so stale-worktree execution cannot be distinguished from stale presentation.

### Goal

After any background push attempt, the push popup shows the exact process argv and working directory, explicit exit status or setup/cancellation reason, and separately retained stdout and stderr. After a zero exit status, Gitti immediately requests a post-push state reconciliation that cannot be dropped behind an in-flight refresh. The popup becomes finally successful only after that reconciliation has completed or reports that the push succeeded but refresh failed. On a successful reconciliation, the current branch/upstream, remote branch list, ahead/behind counters, and Commit Log decorations all describe the post-push repository generation.

**Non-goals:**

- Changing normal push into `--force-with-lease`, retrying a rejected push, or otherwise choosing a different push mode.
- Changing `push.default`, push refspec resolution, remote selection, first-push `-u` behavior, or upstream naming. Existing Git semantics remain authoritative.
- Fetching after a successful push. The reconciliation reads local state written by Git; it does not add a second network operation.
- Treating a presentation-refresh failure as a failed push or rolling back a remote update.
- Persisting command output, adding telemetry, or exporting working-directory/output data to the log file. Diagnostics remain in memory for the popup's existing lifetime.
- General replacement of the daemon's watcher or refresh architecture outside the branch, remote-sync, and Commit Log domains needed by this capability.

### Use cases

- A developer whose tracked feature branch is four fast-forward commits ahead chooses **Push** and immediately sees `0↑ 0↓` and `*feature^` at `HEAD`, without force pushing or manually fetching.
- A developer sees a rejected or failed push with the exact argv, cwd, exit status, stdout, and stderr in the push popup rather than inferring failure from a red border.
- A developer running Gitti in a linked worktree can verify that the push executed in that worktree and sees the shared remote-tracking ref reflected in that worktree's Commit Log.
- A repository refresh already in flight when push completes performs a later pass instead of consuming and losing the post-push request.

## 2. Design

### Push invocation and result contract

Extract one pure push-argument builder in `api/git/commit.go` and use it from both `GitPush` and `GitPushWithSigning`. Its inputs are the selected remote, push mode, current branch, and the already-probed upstream-exists boolean. It preserves the current matrix exactly:

| Upstream | Mode | Operation arguments |
| --- | --- | --- |
| present | normal | `push --progress <remote>` |
| present | safe force | `push --progress --force-with-lease <remote>` |
| present | dangerous force | `push --progress --force <remote>` |
| absent | any | the corresponding command with `-u` and the current branch operand, as today |

The background execution path returns a `GitPushResult` rather than an integer. The result owns defensive copies of:

- `Argv`: `exec.Cmd.Args`, including the `git` executable and executor-injected arguments, so it is the process argv rather than a reconstructed shell command;
- `WorkingDirectory`: `exec.Cmd.Dir` captured before start;
- `Started`, `ExitCode`, `Cancelled`, and `Err`, with `ExitCode = -1` only when no process status exists;
- exact stdout and stderr byte streams, kept separate.

The command uses separate stdout and stderr pipes. Two readers drain them concurrently while retaining their bytes and emitting stream-tagged progress records for the live viewport. Process wait and reader completion are ordered so neither pipe can fill and block the child. Pipe creation, start, read, wait, cancellation, and lock refusal each produce a typed result and an existing-style log entry; no path returns an unexplained sentinel. A nonzero `exec.ExitError` preserves its actual exit code. Cancellation remains a distinct outcome and the existing cancelled popup guard prevents a late result from reopening or recolouring a closed popup.

`GitPushResult` is immutable after publication. Accessors continue returning copies so the API goroutine cannot race the TUI over output storage. Starting a push clears every field from the preceding attempt.

### Diagnostic popup

While the command is active, the viewport continues to show progress as it arrives, now tagged by source where both streams are used. When execution ends, the popup is rebuilt into deterministic sections:

```text
Working directory: <cwd>
argv[0]: "git"
argv[1]: "..."
...
Exit status: <integer | not started | cancelled>

stdout:
<verbatim stdout, or an explicit empty marker>

stderr:
<verbatim stderr, or an explicit empty marker>
```

One quoted argument per line makes spaces, empty arguments, and option boundaries unambiguous without suggesting that Gitti invoked a shell. The retained bytes are the evidence; rendering preserves stream content and the existing progress/ANSI policy rather than merging streams. The popup keeps red for command failure and green for a successful push plus successful reconciliation. If Git succeeds but reconciliation fails, it shows a success statement and a visible refresh warning; it never labels the remote update as failed.

All added labels and status text are fields in `i18n/types.go` with entries in `en.go`, `ja.go`, `zh-hans.go`, and `zh-hant.go`. The exact argv, cwd, status, and stream bytes are not localized.

The signing-required route remains terminal-interactive through `tea.ExecProcess`. It reuses the same argument builder, sets `exec.Cmd.Dir` to the active `m.RepoPath` before suspension, leaves terminal output directly visible, and carries enough operation identity and success information in its completion message to request the same post-push reconciliation. Setting the directory closes the existing in-process worktree-switch ambiguity without redirecting an interactive signing prompt into the background popup.

### No-drop post-push reconciliation

Add generation-aware daemon refresh coordination for the three independently runnable state domains affected by push:

1. **Local branch:** `GitBranch.GetLatestBranchesInfo`, then `GIT_BRANCH_UPDATE`, so `m.CheckOutBranch` is reloaded from the current worktree.
2. **Remote/upstream local read:** read upstream identity and ahead/behind counts plus `GitBranch.GetLatestRemoteBranchesInfo`, then emit `GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE`. This state pass never performs network I/O.
3. **Commit Log:** `GitCommitLog.GetCommitLogs`, then `GIT_COMMITLOG_UPDATE`, so `%D` is read again after the remote-tracking ref moved and compact rendering can place `^` at the new tip.

Each state domain has monotonically increasing requested and completed generations and at most one state-publication worker. A request made while that worker is active advances the requested generation; before the worker retires it must run again until completed catches requested. This replaces the current skip-on-busy behavior only for these three domains. The domains remain concurrent with each other.

Remote network fetch is separated from remote-state publication instead of encoding `needFetch` and `userTriggered` into the state generation. Existing startup, timer, and manual-fetch requests retain their current fetch policy and logging through a fetch coordinator; when a fetch finishes, it requests a remote/upstream local-read generation. Watcher and post-push callers request the local read directly. A post-push local-read request is never OR-merged into, satisfied by, or queued behind a pending fetch request: it can run while network fetch is active, and a fetch that completes later requests another local read. Thus the post-push ticket neither suppresses periodic/manual fetch behavior nor waits for unrelated network I/O.

`RequestPostPushRefresh` returns a ticket tied to the Git-operations/worktree generation that executed the push and to one target generation in each of the three state domains. A ticket completes when each target is satisfied by a pass whose start follows the ticket request and that pass has emitted its TUI update; it does not wait for a worker to become globally idle or for newer unrelated requests. If the daemon's current Git-operations generation no longer matches, every affected worker checks again before storing a snapshot and emitting its event, and the ticket reports a refresh failure instead of publishing old-worktree results into a newly selected worktree. This is defensive for the signing/resume path; normal popup interaction already prevents a worktree switch during push.

The relevant refresh methods return errors and assemble complete replacement snapshots in local variables before one publication. In particular, upstream identity is not stored before ahead/behind parsing succeeds, and a failed post-push read does not install a partial branch list, partial Commit Log, or empty ahead/behind snapshot over valid prior data. The ticket joins domain failures for the popup/log warning. Successful domains may still publish their newer state; the warning names any domain that did not reconcile.

The background push service follows this sequence:

```text
run push
  ├─ nonzero / setup failure / cancellation → publish final push result; no post-success ticket
  └─ exit 0
       → mark popup stage as “refreshing repository state”
       → await RequestPostPushRefresh ticket
       → publish final result with push success and refresh outcome
```

The signed-push completion path requests the same ticket after a successful `tea.ExecProcess` result and logs any reconciliation failure because no push popup remains after terminal resume.

### Compatibility, failure handling, security, and rollback

Git command selection and remote mutation semantics do not change. Normal, safe-force, dangerous-force, first-push, signed, and unsigned paths share argument construction, preventing diagnostic work from changing one route independently. Git remains responsible for updating the matching local remote-tracking ref after a successful push; Gitti reads that ref but does not synthesize or force-update it.

A failed or cancelled push does not claim success and does not start the success-only reconciliation. A successful push followed by a read failure remains a successful push, with a separate warning and last-good UI state. There is no persisted schema or migration. Rollback is a code revert; remote updates already accepted by Git are not reversible application state.

Argv and cwd are rendered as data and are never reparsed or passed through a shell. The push command contains a configured remote name, not its credential-bearing URL. Output remains ephemeral and is not added to exported logs by this work, limiting accidental persistence of hook or server messages. Existing credential prompting remains disabled in the background executor, and signing-required interaction retains its terminal route.

## 3. Implementation

### Phase 1: Expose definitive push results

**ID:** `1`
**Goal:** every background push option produces one observable, unambiguous process result without changing what Git is asked to push
**Tests:** `api/git/commit_test.go`, `i18n/i18n_test.go`, `tui/popup/push/push_test.go`, `tui/utils/utils_test.go`

**Acceptance criteria:**

- [ ] One pure builder produces the existing normal, safe-force, and dangerous-force operation arguments for tracked and untracked branches, and both background and signing paths use it; the signing command executes with `Dir` equal to the active model repository path.
- [ ] A background result records the actual `exec.Cmd.Args`, `cmd.Dir`, whether the process started, exact exit code when available, cancellation/error state, and separate stdout/stderr bytes.
- [ ] Stdout and stderr are drained concurrently without deadlock; read, pipe, start, wait, lock-refusal, nonzero-exit, and cancellation outcomes are distinguishable and logged.
- [ ] The live viewport still receives progress, and the completed popup visibly renders cwd, quoted argv elements, explicit status, stdout, and stderr; empty streams are explicit rather than blank ambiguity.
- [ ] A second push cannot display command, status, or output retained from the first.
- [ ] Nonzero and setup failures show actionable text in the popup in addition to a red border, while cancellation does not mutate a popup the user already closed.
- [ ] Added user-facing labels exist in all four locale mappings and locale parity remains green.

**Steps:**

1. Refactor push argument construction and add the result/output record types in `api/git/commit.go`; adapt logging and defensive output accessors.
2. Replace the combined pipe with separately drained stdout/stderr capture while preserving progress notifications.
3. Carry the full result through `tui/services/push.go` and `tui/types/event_types.go`.
4. Update `tui/popup/push/{types,init,update,render}.go` for live stream records and deterministic completed diagnostics.
5. Add locale fields/values and focused API, service/popup, and locale regression tests.

### Phase 2: Reconcile successful pushes without dropped refreshes

**ID:** `2`
**Goal:** a successful push in the active worktree deterministically publishes post-push branch, upstream, ahead/behind, remote-branch, and Commit Log state
**Tests:** pending

**Acceptance criteria:**

- [ ] A zero-status background push enters a visible reconciliation stage and publishes its final success only after a post-push refresh ticket completes; failed, unstarted, and cancelled pushes do not request that ticket.
- [ ] A successful signing-required push requests the same reconciliation after terminal resume.
- [ ] A request arriving before, during, or after an active state refresh causes every required domain to perform a pass begun after the request; no request is lost to a busy guard, and the ticket waits only for its target passes rather than global worker idleness.
- [ ] Post-push reconciliation performs no fetch and does not wait for an active or pending network fetch; startup, timer, and manual-fetch policy/logging remain unchanged, and fetch completion schedules its own later remote-state read.
- [ ] The reconciliation does not change normal push into a force operation or alter first-push/upstream semantics.
- [ ] Successful reconciliation emits branch, remote-sync/upstream, and Commit Log update events, yielding current checkout/upstream data, `0↑ 0↓` for a synchronized tracked branch, and the remote marker at the pushed tip.
- [ ] Refresh errors preserve last-good domain snapshots, identify failed domains in the popup/log, and do not relabel the successful Git push as failed.
- [ ] A stale worktree/Git-operations generation is rejected at snapshot publication/event emission and reported rather than publishing results into the newly active worktree.
- [ ] An integration fixture with a bare local remote and a linked worktree covers an existing tracked feature branch several fast-forward commits ahead: normal push moves the remote and remote-tracking refs to `HEAD`, reconciliation reaches zero ahead/behind, and Commit Log decorations place local and remote at the same commit.
- [ ] Concurrency tests cover a post-push request made during each affected state pass and during an active network fetch, proving that a later local state generation runs, the ticket does not wait for that fetch, and fetch completion still schedules its own read.

**Steps:**

1. Add generation-aware state refresh coordination in `api/daemon.go` for local branch, remote/upstream local reads, and Commit Log domains; route their existing state-read requests through it.
2. Separate fetch scheduling from remote-state publication while preserving startup/timer/manual fetch flags and logging; fetch completion requests a local remote-state generation, while post-push requests never join the fetch queue.
3. Make the relevant refresh calls report errors while publishing only complete snapshots and preserving last-good state on failure.
4. Add the post-push ticket and worktree-generation checks at publication, with update events emitted by the completed target passes.
5. Integrate the ticket into `tui/services/push.go`, the popup stage/result model, and the successful push-signing completion handling in `tui/tui.go`.
6. Add deterministic state/fetch scheduler, service, signing-route, failure, and linked-worktree fast-forward integration tests.

### Phase 3: Full test sweep

**ID:** `3`
**Goal:** every test declared by this spec is green together
**Tests:** all

**Acceptance criteria:**

- [ ] The union of test paths declared by completed phases passes through the scoped resolver.
- [ ] Failures surfaced by the sweep are remediated in this phase.

### Phase 4: Outcome

**ID:** `4`
**Goal:** record the delivered push diagnostics and refresh guarantees against issue #7
**Tests:** none — this phase reconciles evidence and authors no implementation tests

**Acceptance criteria:**

- [ ] `Outcome` records delivered behavior, deviations, decisions, deferred work, and the Full test sweep result.
- [ ] Any remaining Open Question is resolved or explicitly deferred before the outcome is finalized.

### Phase 5: Documentation

**ID:** `5`
**Goal:** document the shipped push result and post-success refresh behavior in the repository's established documentation system
**Tests:** none — documentation placement and prose verification do not author automated tests

**Acceptance criteria:**

- [ ] `$ckit:docs` is invoked through its build-owned route for the shipped behavior and verification instructions.
- [ ] Existing documentation is reconciled where present; otherwise the nearest relevant README is used, falling back to the root README.
- [ ] User guidance distinguishes a successful push from a subsequent refresh warning and makes clear that normal fast-forward push never requires force as a refresh mechanism.

## 4. Verification

- [ ] In a linked-worktree fixture with an existing `origin/feature/x` upstream four commits behind local `feature/x`, choose normal **Push** and verify the displayed argv contains no force option, cwd is the linked worktree, exit status is zero, remote and remote-tracking refs equal local `HEAD`, the status panel reads `0↑ 0↓`, and the Commit Log tip renders `*feature/x^`.
- [ ] Hold each affected state domain in flight as push completes; release it and verify a later post-request pass runs before the ticket resolves and the UI receives all three update events.
- [ ] Hold a network fetch in flight as push completes and verify the post-push remote-state pass and ticket complete without waiting for that fetch; then release the fetch and verify its own follow-up state read still runs.
- [ ] Reject a normal push from the remote side and verify the nonzero status and stderr are visible, the popup is red, no success-only refresh ticket is issued, and the pre-push refs/counters remain truthful.
- [ ] Force a post-push read failure after a successful local-remote push and verify the popup states that Git succeeded, names the refresh failure, and keeps the last complete UI snapshot.
- [ ] Repeat success through the safe-force, dangerous-force, and signing-required routes to verify shared refresh behavior without changing their respective argv or terminal-interaction policy.
- [ ] Verify the first-push path still uses `-u <remote> <current-branch>` and no new fetch, retry, or force behavior is introduced.

## 5. Open Questions

No unresolved design questions remain. First-push publishing improvements, push-policy changes, persistent operation transcripts, and general daemon refresh redesign are explicit non-goals rather than blockers.

## 6. Advisory Sweep

| Track | Status | Configured model | Actual model | Effort | Verdict | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| `codex` | ran | `gpt-6-astra` | `gpt-6-astra` | high | sound | Found the command/result contract, target-generation refresh tickets, worktree rejection, phase ordering, and concrete test oracles build-ready; emphasized testing pass-start order and complete-snapshot preservation. |
| `grok` | ran | `grok-4.6` | `grok-4.6` | medium | needs-attention | Identified that a generation-only remote worker left fetch-capable timer/manual requests ambiguous and could make a no-fetch ticket wait on network I/O. The design now separates network fetch scheduling from remote-state generations, gives post-push tickets explicit no-fetch target passes, and checks worktree generation at publication. |

Both configured tracks ran with their configured models and efforts; there are no disabled, unavailable, malformed, or user-accepted coverage gaps. The supported `grok` defect was incorporated before handoff.

## Outcome

<!-- Build fills this after implementation, including the full-sweep result. Keep it brief. -->
