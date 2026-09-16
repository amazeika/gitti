---
status: in-progress
issue: 8
pr: null
completed: [1, 2]
---

# Local-Only Branch Status and Publishing — Design Document

Issue [#8](https://github.com/amazeika/gitti/issues/8) reports that an attached local branch without an upstream is rendered as a red remote-sync failure even though the repository is healthy. This change gives upstream observation an explicit state, renders unpublished branches neutrally, and turns the existing `p` workflow into a clear, safe **Publish Branch** path that assigns an upstream and immediately reuses the post-push reconciliation delivered for issue #7.

## 1. Motivation

### Current state

`GitRemote.GetLatestRemoteSyncStatusAndUpstream` in `api/git/remote.go` asks `hasUpstreamWithIcon` for an upstream and, when none is returned, publishes empty upstream and ahead/behind strings. That correctly avoids running `git rev-list --left-right --count HEAD...@{upstream}` for the normal no-upstream case, but the snapshot has no field that says why its values are empty. `GittiModel` copies only the two strings, upstream name, and icon, and `renderGitStatusComponentPanel` in `tui/layout/render.go` renders a red cross whenever either count is empty. The same representation therefore conflates startup, detached or unborn repositories, an attached unpublished branch, and an unexpected upstream-resolution failure.

The upstream probe in `api/git/utils.go` also reduces every failed `git rev-parse --abbrev-ref @{u}` to `false`. The push argument builder then treats that boolean as “no upstream” and adds `-u`, so an actual inspection failure can be mistaken for authorization to establish new tracking configuration.

Remote discovery has the same ambiguity: `GitRemote.CheckRemoteExist` returns `false` for both a successful empty inventory and command failure. It publishes three mutable slices without synchronization or defensive copies and groups `git remote -v` rows by name plus URL, so one configured remote with multiple push URLs can appear as several destinations. Push argv also places the configured remote name in option-parsed position; avoiding a shell does not stop a leading-hyphen name from being interpreted as a Git option.

A global `p` action and most of the publication mechanics already exist. `tui/interaction/handler/nontyping/p.go` discovers push-capable remotes, defaults when there is one, prompts when there are several, and opens the push-type chooser. The shared push builder in `api/git/commit.go` adds an upstream on a first push, and issue #7 added definitive push diagnostics plus a success-only reconciliation of branch, remote/upstream, remote-branch, and Commit Log state. What is missing is an explicit unpublished state and a discoverable publish-only route; today an unpublished branch is shown as broken and is offered the same normal/safe-force/dangerous-force choices as a tracked branch.

### Goal

An attached branch with commits and no configured upstream is shown as a neutral, localized **Local only** state rather than an error. Pressing `p` exposes **Publish Branch**, selects the sole push-capable remote or asks the user when several exist, and performs a normal `git push --set-upstream <remote> HEAD`. A successful publication uses the existing no-drop post-push ticket so the header, upstream identity, ahead/behind counts, remote list, and Commit Log decoration update before the operation is finally marked successful. Unexpected upstream-discovery or count failures remain visibly different from an unpublished branch and never cause Gitti to infer a first push.

**Non-goals:**

- Automatically creating a remote, inventing a remote URL, or publishing immediately after the Add Remote form succeeds; repositories with no remote retain the explicit add-then-publish flow and the existing `origin` prefill.
- Automatically choosing among multiple push-capable remotes, even when one is named `origin`; the chooser may preselect `origin`, but the user confirms the destination.
- Offering force-push as a way to publish a branch for the first time, changing force-push policy for tracked branches, retrying rejected pushes, or changing credential/signing behavior.
- Publishing detached `HEAD`, an unborn branch with no commit, another branch selected in the branch list, or every local-only branch in bulk.
- Fetching as part of publication, synthesizing remote-tracking refs, or rolling back a remote update that Git has already accepted.

### Use cases

- A developer checks out `feature/177-package-install` before its first push and sees **Local only** beside the local branch identity, with no red error indicator.
- The developer presses `p`; with one push-capable remote, Gitti offers a single **Publish Branch** confirmation, then establishes the upstream with a normal push.
- In a repository with several push-capable remotes, the developer chooses the intended destination before confirming publication; `origin` is initially selected when present.
- After publication, the developer immediately sees the upstream name, `0↑ 0↓`, and the matching remote decoration at the published commit.
- If upstream discovery or ahead/behind inspection fails unexpectedly, the header shows an unavailable/warning state and `p` does not reinterpret the failure as an unpublished branch.

## 2. Design

### Explicit upstream observation contract

Replace empty-string inference with a typed upstream-observation state carried by the immutable remote-sync snapshot in `api/git/remote.go`. The exported combined snapshot remains the single read boundary and gains the state, while ahead/behind values remain valid only for `tracked`.

| State | Meaning | Header behavior | `p` behavior |
| --- | --- | --- | --- |
| `pending` | No observation for this Git-operations/worktree generation has completed yet. | Neutral loading marker; never a red failure. | Do not infer publication; ask the user to wait for repository state. |
| `tracked` | The attached branch has a resolvable upstream and `rev-list` produced valid counts. | Existing localized upstream identity and ahead/behind display. | Preserve the existing normal/safe-force/dangerous-force push flow. |
| `unpublished` | An attached branch with a commit is authoritatively known to have no configured upstream. | Neutral localized **Local only** label and the local branch name. | Offer the publish-only flow. |
| `not-applicable` | There is no publishable current branch, including detached `HEAD` or an unborn branch without a commit. | Neutral localized non-applicable label; no red failure. | Do not open a publish flow. |
| `unavailable` | Upstream identity, configured tracking, or ahead/behind state could not be inspected reliably. | Warning/error styling distinct from **Local only**; keep details in the log. | Refuse to guess that the branch is unpublished. |

Introduce one upstream resolver used by both remote-state refresh and push construction. It verifies the current symbolic branch and commit, distinguishes a missing configured upstream from command/start/parse failures, and returns a typed result rather than a `(string, bool)` pair. A configured but missing or otherwise unresolvable upstream is `unavailable`, not `unpublished`. Once an upstream is resolved, `git rev-list --left-right --count HEAD...@{upstream}` must produce exactly two non-negative counts; execution or parse failure is also `unavailable`.

Every observation includes the exact symbolic local-branch identity it classified. A successful read atomically publishes that identity, its state, and matching payload after the existing worktree-generation guard. On a failed read, the guarded publication changes only observation health to `unavailable` and retains the previous complete branch/upstream/count payload internally as last-good data; consumers hide that payload while health is unavailable. The method still returns an error so post-push tickets and logs report the failure. The daemon emits `GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE` when a current-generation health observation was published, including `unavailable`, while still failing a reconciliation ticket as appropriate. A stale worktree generation publishes neither data nor health and emits no stale event.

`pending` is installed by both `InitGittiModel` and `ReinitGittiModel`, preventing empty startup fields from masquerading as a read failure. `GittiModel` stores the typed state and observed branch alongside the existing display fields, and `updateGitRemoteStatusSyncLineStringAndUpStream` copies the combined snapshot in one operation. The `p` handler may act on `unpublished` only when the observation's branch equals the model's current canonical checkout branch; a mismatch is treated as pending state and requests/awaits a fresh observation rather than combining independent branch and remote events.

### Status rendering and localization

`renderGitStatusComponentPanel` switches on the typed state instead of checking whether count strings are empty:

- `tracked` renders the current green local/red remote counters and upstream identity;
- `unpublished` renders **Local only** in a neutral informational style and uses the checked-out local branch identity;
- `not-applicable` and `pending` render concise neutral labels without claiming publication is possible;
- `unavailable` renders a warning/error marker and localized **Upstream unavailable** text, not stale counts.

The status prefix is measured with `lipgloss.Width` before truncating the repository/branch portion, so all screen modes and narrow widths retain valid geometry. New labels and the context-specific `[p] publish branch` hint are fields in `i18n/types.go` with values in `en.go`, `ja.go`, `zh-hans.go`, and `zh-hant.go`; locale parity remains enforced. Detailed Git errors stay in the existing log rather than being placed in the one-line header.

The global help continues to describe `p` as push/publish. When the Git Status panel is focused, its otherwise minimal keybinding bar advertises `[p] publish branch` only for `unpublished`; other states do not promise an unavailable action.

### Publish-only interaction

The `p` handler branches on the latest typed observation:

1. For a branch-matching `unpublished` observation, request a fresh, fallible snapshot of configured remotes. The reader returns an immutable generation containing one entry per unique remote name and its complete fetch/push URL sets; execution or parse failure keeps the last-good inventory and opens an actionable warning, never Add Remote.
2. Only a successful empty inventory opens the existing Add Remote form prefilled with `origin`; adding a remote does not implicitly push.
3. With exactly one push-capable remote name, use it as the proposed destination and open a one-choice **Publish Branch** confirmation. Multiple `pushurl` values remain one destination because `git push <name>` intentionally applies Git's configured multi-push-URL semantics.
4. With multiple push-capable remote names, open the existing remote chooser, preselect `origin` when present, and then open the same publish confirmation for the chosen remote.
5. Confirmation starts only the normal publish route. Safe-force and dangerous-force choices are not shown for an unpublished branch.

Remote inventory parsing builds and validates a complete local result before one atomic publication. Readers receive immutable values or defensive copies. Malformed nonempty output fails the read; a successful command with no configured names is the only empty-inventory result. Ordering is deterministic, and race coverage exercises daemon refresh concurrent with handler reads.

For `tracked`, `p` retains the existing push-type and remote-selection behavior. For `pending`, `not-applicable`, or `unavailable`, it opens no push chooser and exposes a localized, actionable reason rather than allowing empty state to select a first-push command. Popup models carry an explicit push intent (`push` or `publish`) so a delayed Enter event cannot derive mutation semantics from whatever the header happens to show later.

At execution time, the API resolver and fallible inventory reader run again under the selected Git-operations/worktree generation. They verify that the generation is still active, the attached branch is still the branch captured by the UI, and the selected remote remains push-capable. A push-safe remote name is nonempty and does not begin with `-`; option-like names are refused before command construction because the settled argv keeps the remote in option-parsed position. For `unpublished`, the shared argument builder constructs the normal operation as:

```text
push --progress --set-upstream <remote> HEAD
```

Each `GitOperations` generation owns an immutable command worktree/top-level path used by its upstream resolver, inventory reader, background push, and signed push. These operations do not consult the mutable global executor path after the generation is created. Immediately before process start, an execution guard verifies that the captured generation is still active; branch drift, worktree-generation drift, a detached/unborn state, unavailable inspection, an unsafe or removed remote, or inventory failure produces an unstarted definitive result. If only the branch has become tracked while the same generation and remote remain valid, the normal route may proceed without `--set-upstream`. This prevents a stale dialog, stale upstream snapshot, global path switch, or failed probe from mutating the wrong repository or assigning tracking configuration to the wrong branch.

The signing-required route uses the same resolver, inventory validation, argument builder, captured generation, and immutable working directory before terminal suspension. Remote names are passed as argv rather than through a shell, but argv construction alone is not considered validation.

### Success, failure, compatibility, and rollback

A zero-exit publication uses issue #7's `RequestPostPushRefresh` ticket. The popup remains in its repository-state reconciliation stage until branch, remote/upstream, remote-branch, and Commit Log passes satisfy that ticket. The remote/upstream pass then publishes `tracked`, the assigned upstream, and current counts—normally `0↑ 0↓` for a newly created same-name branch—and the Commit Log rereads the new remote-tracking decoration. No fetch is introduced.

A rejected, cancelled, unstarted, or nonzero push leaves the branch `unpublished`; Git remains authoritative over whether any remote ref was created. A successful remote update followed by refresh failure remains a successful push with a separate warning, as in issue #7; the next successful state pass can recover the header. There is no persisted schema or migration. Rollback is a code revert, but it cannot undo a remote branch already accepted by Git.

Tracked-branch normal and force push behavior, push diagnostics, cancellation, worktree-generation checks, and signed terminal interaction remain compatible. The only intentional first-push behavior change is replacing the branch-name operand with the equivalent `HEAD` source and presenting only normal publication, making the action explicit and preventing force from being selected as first-push policy.

## 3. Implementation

### Phase 1: Distinguish and render upstream states

**ID:** `1`
**Goal:** every repository generation presents a truthful, typed upstream state, with a local-only branch visibly distinct from loading, non-applicable, and failed inspection
**Tests:** `api/daemon_test.go`, `api/git/refresh_snapshot_race_test.go`, `api/git/remote_test.go`, `tui/initialize/initialize_test.go`, `tui/layout/render_test.go`

**Acceptance criteria:**

- [ ] One resolver classifies tracked, unpublished, detached, unborn, and unexpected-failure cases without treating an arbitrary Git command failure as “no upstream.”
- [ ] The atomic combined remote snapshot carries the observed symbolic branch and observation state with its matching upstream/count payload; tracked counts are validated, and concurrent readers never observe a mixed generation.
- [ ] The UI enables publication only when the snapshot's observed branch matches the model's canonical checkout branch; out-of-order branch and remote events cannot expose publication for stale state.
- [ ] A failed current-generation read publishes `unavailable` health, preserves last-good payload, emits a UI update, logs the cause, and still returns an error; a stale-worktree read publishes and emits nothing.
- [ ] Startup and worktree reinitialization begin at `pending`, then transition on the first completed state pass.
- [ ] The header renders localized neutral **Local only** for an attached committed branch without upstream, tracked counters only for `tracked`, neutral pending/non-applicable states, and a distinct warning for `unavailable`.
- [ ] Header truncation remains within its width budget in two-column, single-column, and focused modes for each state and locale.
- [ ] Locale mappings remain structurally complete in English, Japanese, Simplified Chinese, and Traditional Chinese.

**Steps:**

1. Add the typed resolver and observation state to `api/git/utils.go` and `api/git/remote.go`, retaining atomic last-good payload semantics and extending focused API/daemon race and failure tests.
2. Adapt the remote/upstream state pass in `api/post_push_refresh.go` to publish current-generation health updates while preserving ticket failure reporting.
3. Carry the state through `tui/types/types.go`, `tui/initialize/initialize.go`, and `tui/tui.go`.
4. Replace empty-string rendering inference in `tui/layout/render.go`, add width/state table tests, and add localized status text and hints in `i18n/{types,en,ja,zh-hans,zh-hant}.go`.

### Phase 2: Publish a local-only branch safely

**ID:** `2`
**Goal:** a user can publish the current local-only branch through one explicit normal-push path and see its upstream state reconcile immediately
**Tests:** `api/daemon_test.go`, `api/daemon_publish_test.go`, `api/git/commit_test.go`, `api/git/publish_test.go`, `api/git/remote_test.go`, `tui/popup/push/push_test.go`, `tui/services/publish_integration_test.go`, `tui/services/push_service_test.go`, `tui/utils/utils_test.go`

**Acceptance criteria:**

- [ ] Pressing `p` on `unpublished` offers a publish-only confirmation; it does not expose safe-force or dangerous-force choices.
- [ ] A fallible, atomically published remote inventory returns one immutable entry per configured remote name with complete fetch/push URL sets; only a successful empty inventory opens Add Remote, one push-capable name is proposed directly, and multiple names require a choice with `origin` initially selected when available.
- [ ] Multiple push URLs on one configured remote produce one chooser destination and retain Git's named-remote multi-URL behavior; command/parse failure is reported and never treated as no remotes.
- [ ] Concurrent daemon inventory refresh and UI reads remain race-free and cannot expose a partial generation.
- [ ] The confirmed API operation re-resolves state, verifies the captured branch and configured push remote, rejects empty or leading-hyphen remote names, and invokes normal `push --progress --set-upstream <remote> HEAD` only while the branch is still unpublished.
- [ ] A branch that became tracked before confirmation uses normal tracked push semantics, while branch drift, worktree-generation drift, detached/unborn state, unavailable inspection, an unsafe or removed remote, or inventory failure produces an unstarted actionable result and no mutation.
- [ ] Each Git-operations generation immutably binds the resolver, inventory reader, background push, and signing-required push to its own worktree; a switch before process start is refused, and neither route consults the mutable global executor path after capture.
- [ ] Background and signing-required publication share the same resolver, inventory validation, argument builder, and execution guard while retaining issue #7's diagnostics and cancellation behavior.
- [ ] A successful publication waits for the existing no-fetch post-push ticket and then shows the assigned upstream, synchronized ahead/behind counts, refreshed remote inventory, and matching Commit Log decoration without manual refresh.
- [ ] Failed publication leaves the branch visibly local-only; a successful push followed by reconciliation failure remains successful with a separate warning and later state passes can recover.
- [ ] Tracked branches retain their existing remote selection and normal/safe-force/dangerous-force workflows.
- [ ] An integration fixture with a bare local remote covers publishing an attached local-only feature branch from a linked worktree through the UI service path and verifies the remote ref, upstream config, `0↑ 0↓`, and remote decoration at `HEAD`.

**Steps:**

1. Add explicit publish intent to the push/remote popup flow and branch `tui/interaction/handler/nontyping/p.go` plus Enter handling by branch-matching observation state.
2. Replace boolean/mutable remote discovery in this path with a fallible immutable inventory grouped by configured name, then reuse the chooser with deterministic `origin` preselection and a one-choice publish confirmation.
3. Bind the relevant API handlers to their `GitOperations` generation's immutable worktree and update `api/git/commit.go` and `tui/services/push.go` so background and signed operations revalidate generation, branch, upstream, push-safe remote name, and inventory before sharing the publish builder.
4. Add focused interaction, API, inventory race/parser, option-like remote, multi-`pushurl`, service, popup, signing, generation-drift/failure, and linked-worktree publication tests.

### Phase 3: Full test sweep

**ID:** `3`
**Goal:** every test declared by this spec is green together
**Tests:** all

**Acceptance criteria:**

- [ ] The union of test paths declared by completed phases passes through the scoped resolver.
- [ ] Failures surfaced by the sweep are remediated in this phase.

### Phase 4: Outcome

**ID:** `4`
**Goal:** reconcile delivered local-only status and publication behavior against issue #8
**Tests:** none — this phase reconciles delivery evidence and authors no implementation tests

**Acceptance criteria:**

- [ ] `Outcome` records delivered behavior, deviations, decisions, deferred work, and the Full test sweep result.
- [ ] Every Open Question is resolved or explicitly deferred before Outcome is finalized.

### Phase 5: Documentation

**ID:** `5`
**Goal:** document local-only status, Publish Branch behavior, destination selection, failures, and verification in the repository's established documentation system
**Tests:** none — documentation placement and prose verification do not author automated tests

**Acceptance criteria:**

- [ ] `$ckit:docs` is invoked through its build-owned route for the shipped behavior and verification instructions.
- [ ] Existing documentation is reconciled where present; otherwise the nearest relevant README is used, falling back to the root README.
- [ ] User guidance distinguishes **Local only** from an upstream inspection warning, explains `p` destination selection and the no-remote flow, and states that publication uses a normal push followed by a local no-fetch reconciliation.

## 4. Verification

- [ ] Start Gitti on an attached committed branch with no upstream and verify the header says **Local only** without a red cross in two-column, single-column, and focused modes.
- [ ] Publish that branch to a sole local bare `origin`; verify the captured argv contains `push --progress --set-upstream origin HEAD`, the upstream becomes `origin/<branch>`, ahead/behind becomes `0↑ 0↓`, and the Commit Log shows the local and remote tip together before the popup becomes finally successful.
- [ ] Repeat with two push remote names and verify the chooser initially highlights `origin` but does not proceed until a destination is confirmed; configure two push URLs on one name and verify it remains one destination.
- [ ] With no remote, press `p`, add an `origin`, and verify no network mutation occurs until `p` is invoked and publication is confirmed again; force inventory command and parse failures and verify neither opens Add Remote.
- [ ] Concurrently refresh and read remote inventory under `go test -race`; verify readers see only complete, unique-name generations.
- [ ] Configure an option-like remote name and verify publication is refused before process start rather than interpreting it as push options.
- [ ] Force upstream discovery and `rev-list` failures separately; verify both show **Upstream unavailable**, log the cause, preserve last-good payload internally, and never construct a `--set-upstream` push.
- [ ] Deliver branch and upstream events out of order and verify a stale `unpublished` observation cannot enable publication for the new branch.
- [ ] Open Publish Branch, then change the checked-out branch, switch worktrees, or remove the selected remote externally before confirming; verify no push process starts, the captured generation's immutable workdir is never retargeted, and the popup reports the stale intent.
- [ ] Verify detached `HEAD` and an unborn branch render neutral non-applicable state and cannot enter publication.
- [ ] On a tracked branch, verify `p` still offers normal, safe-force, and dangerous-force choices and successful pushes retain issue #7's reconciliation behavior.

## 5. Open Questions

No unresolved design questions remain. Automatic publication after adding a remote, publishing detached/unborn states, and force-first-push policy are explicit non-goals rather than blockers.

## 6. Advisory Sweep

| Track | Status | Configured model | Actual model | Effort | Verdict |
| --- | --- | --- | --- | --- | --- |
| `astra` | ran | `gpt-5.6-sol` | `gpt-5.6-sol` | high | `needs-attention` |

The final advisory identified option-like remote-name injection, ambiguous and racy remote inventory, mutable global worktree binding, and stale cross-domain branch identity as implementation blockers. The design and acceptance criteria above incorporate all four findings. Advisory artifact: `.ckit/spec-advisory/issue-8/astra-advice.json`.

## Outcome

<!-- Build fills this after implementation, including the full-sweep result. Keep it brief. -->
