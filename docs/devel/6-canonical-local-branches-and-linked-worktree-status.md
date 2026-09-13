---
status: in-progress
issue: 6
pr: null
completed: [1, 2, 3]
---

# Canonical Local Branches and Linked-Worktree Status — Design Document

**Derived from:** `.ckit/scratch/https-github-com-amazeika-gitti-issues-6.md`

Gitti currently parses the presentation output of `git branch`, allowing the `+` marker for a branch checked out in a linked worktree to become part of the branch name. That decorated value then reaches filtering, branch actions, the merge chooser, and Git argv, so merging the linked branch fails. This change makes `api/git` publish canonical namespace-relative local-branch identities with separate current/linked-worktree status, renders status only in the TUI, and hardens both merge execution paths with Git's `--` option terminator. It preserves existing branch ordering, current-branch exclusion, merge behavior, and independent commit-log refresh semantics.

## 1. Motivation

### Current state

`GitBranch.GetLatestBranchesInfo` (`api/git/branch.go:88-138`) runs `git branch`, trims whitespace, strips only a leading `*`, and stores every other line directly in `BranchInfo.BranchName`. Git decorates a branch checked out in another worktree as `+ <name>`, so the cache stores presentation text such as `+ skill` instead of the canonical branch name `skill`.

That value crosses several identity boundaries:

- `tui/component/branch/init.go` and `types.go` use `BranchName` for list identity, filtering, selection restoration, and rendering.
- `tui/popup/branch/init.go` copies it into `GitMergeBranchOptionItem`; `update.go` reconstructs both chooser lists by comparing those strings.
- `tui/interaction/handler/nontyping/enter.go` forwards selected strings to both merge paths.
- `GitMerge` and `GitMergeWithSigning` (`api/git/branch.go:379-419`) append the strings directly after `--ff` or `--no-ff`, without an option terminator.

As a result, `git merge --ff '+ skill'` fails because no such ref exists. Fixing this at merge time by trimming `+` would corrupt the valid branch name `+skill`, and it would leave switching, deletion, filtering, and equality dependent on display text. The missing `--` also leaves refs created through plumbing with option-shaped names exposed to argument parsing.

The current refresh mutates `isRepoUnborn`, `currentCheckOut`, and `allBranches` separately. The daemon writes them from a background worker while TUI refreshes read them, and `InitBranchList` loads current and other branches through separate calls. A refresh can therefore expose a partial or mixed-generation view. Successful attached-to-detached transitions can also retain stale current state unless the replacement explicitly clears it.

`api/git/commitlog.go:141-167` already enumerates local refs independently with `for-each-ref` to avoid decorated or stale branch-worker data. Branch and commit-log workers run concurrently in `api/daemon.go`, so shared parsing is useful, but shared mutable snapshots are not.

### Goal

Local branch discovery returns exact names relative to `refs/heads/` and explicit status saying whether each ref is the current worktree's checkout or is occupied by a linked worktree. A complete validated snapshot is published atomically. The local branch panel and merge chooser render `*` and `+` from status while all filtering, comparison, switching, deletion, and merge arguments continue to use the canonical name.

Both background and signing merge paths produce:

```text
git merge <existing strategy options> -- <canonical branch operands in discovery order>
```

This makes linked branches mergeable, preserves literal leading `+`, and prevents option-shaped refs from being interpreted as command options.

**Non-goals:**

- Blocking merge, switch, or deletion solely because a branch is checked out in another worktree; Git remains the authority at execution time.
- Transporting or displaying linked-worktree paths in the branch model.
- Introducing a richer detached-HEAD label or changing branch-panel/title UX beyond removing stale or blank current rows.
- Changing branch sorting, selected-click order, multi-branch merge policy, fast-forward behavior, signing, cancellation, conflict handling, or logging.
- Changing remote-branch discovery or worktree mutation behavior.
- Switching merge operands to fully qualified refs or resolving ambiguity with same-named tags.
- Adding configuration, migration, feature flags, or locale strings.

### Use cases

- A developer sees `+ feature/x` for a branch checked out in another worktree, selects it in the merge chooser, and merges `feature/x` successfully.
- A developer with a real local branch named `+skill` sees `+skill` as its identity (plus a separate status marker when applicable), can filter and select it by that exact name, and passes it unchanged to Git.
- A repository moves from attached HEAD to detached HEAD; the local panel keeps all local refs selectable without showing a stale current branch or a blank row.
- A fresh or orphaned repository retains its symbolic branch as the current unborn branch, including when other local refs still exist.
- A failed or malformed refresh leaves the last complete local-branch snapshot visible rather than publishing partial state.

## 2. Design

### Canonical branch record

Add one stateless `api/git` discovery helper used independently by branch refresh and commit-log refresh. It runs:

```text
git for-each-ref \
  --format=<name>%00<current-conditional>%00<occupied-conditional> \
  refs/heads/
```

The format emits exactly this record per local ref:

```text
name NUL current NUL occupied LF
```

- `name` is `%(refname:lstrip=2)`, for example `feature/x`, never `refs/heads/feature/x` and never a `git branch` marker.
- `current` is a Git format conditional that emits literal `1` only when `%(HEAD)` identifies the current worktree's branch, otherwise literal `0`. The parser does not infer truth from a non-empty `%(HEAD)`, whose non-current representation can contain whitespace.
- `occupied` is a conditional over `%(worktreepath)` that emits literal `1` when any worktree has the ref checked out, otherwise literal `0`.
- Worktree paths are not emitted. Only the boolean needed by callers crosses the command boundary.

The parser consumes bytes without trimming identity. Empty output is a valid empty ref set. Non-empty output must be a sequence of complete three-field records with one terminal LF. It rejects the entire result for an empty name, a missing/interior-short record, an unknown boolean, an extra field, an extra blank record, or more than one `current=1` record. No parsed record is exposed until the entire output validates.

`BranchInfo` retains `BranchName` and `IsCheckedOut`, and gains `IsCheckedOutInLinkedWorktree`. Projection is:

```text
IsCheckedOut = current
IsCheckedOutInLinkedWorktree = occupied && !current
```

The current worktree wins if both source booleans are true, so a row never carries both presentation markers. `BranchName` remains the namespace-relative caller contract used by checkout, delete, rebase, merge, filtering, and equality.

### Snapshot projection and publication

Local state moves into an immutable snapshot containing:

- the current checkout `BranchInfo` (zero-valued when detached);
- all non-current local `BranchInfo` records in discovery order;
- `isRepoUnborn`.

`GitBranch` initializes an empty snapshot and replaces its snapshot pointer once, only after discovery, parsing, and HEAD classification all succeed. Readers receive value copies and defensive slice copies. Existing `CurrentCheckOut`, `AllBranches`, and `IsRepoUnborn` behavior remains available for callers that need one projection. Add a combined snapshot accessor for callers, notably `InitBranchList`, that need current and other branches from one generation.

`AllBranches()` continues to exclude the current worktree's branch. A linked-worktree branch is not current and remains in `AllBranches()`, so existing merge/switch/delete callers continue to see it. Discovery order becomes `for-each-ref`'s deterministic refname order; user-configured `branch.sort` no longer affects this panel. That visible ordering change is intentional, and no additional sort is applied.

A `for-each-ref` execution or parse error logs the failure and keeps the last good snapshot. Discovery remains read-only, does not acquire `GitProcessLock`, and does not retry: refs can legitimately change between discovery and a later user operation.

### Attached, unborn, orphan, and detached HEAD

Classification happens before publication:

| Discovery result | Follow-up | Published state |
| --- | --- | --- |
| Exactly one `current=1` record | None | That record is current, every other record is in `allBranches`, `isRepoUnborn=false`. |
| No current record | Run `git symbolic-ref --quiet --short HEAD` | Continue according to the result below. |
| Symbolic-ref succeeds with one non-empty branch name | None | Publish that name as current and checked out, set `isRepoUnborn=true`, and retain all discovered refs as other branches. This covers fresh and orphan/unborn HEADs, including orphan states that coexist with committed refs. |
| Symbolic-ref returns its documented non-symbolic exit status | None | Publish a detached snapshot: zero current, all discovered refs retained, `isRepoUnborn=false`. |
| Symbolic-ref has any other execution error or malformed output | None | Log the failure and keep the last good snapshot. |

A successful detached refresh must clear stale current state. It does not invent a detached label: `CheckOutBranch` and branch-dependent titles may remain empty until a richer detached UX is designed.

### Commit-log independence

`GitCommitLog.GetCommitLogs` invokes the same stateless local-ref discovery helper itself and projects canonical names from the returned records. It does not read `GitBranch`'s snapshot because the daemon refreshes the two objects concurrently.

Its existing policy remains distinct:

- successful commit-log enumeration publishes canonical local names;
- local-ref enumeration failure clears `localBranchNames` to `nil` so decoration compaction cannot use stale names;
- commit-log graph publication and its existing read-failure policy remain unchanged.

The shared unit is parsing/discovery code, not mutable state or refresh outcome.

### TUI identity and decoration

`InitBranchList` loads one combined local snapshot. It prepends a current row only when the current name is non-empty, then appends `allBranches`. Selection restoration and filtering compare only `BranchName`.

The selection offset is conditional:

- attached or unborn current row present: an `allBranches` index receives `+1`;
- detached current row absent: no offset is added.

`m.CheckOutBranch` is set from that same snapshot, including being cleared on detached HEAD. The branch delegate renders a status column derived from fields:

- `*` for `IsCheckedOut`;
- `+` for `IsCheckedOutInLinkedWorktree`;
- blank otherwise.

`GitBranchItem.FilterValue()` remains exactly `BranchName`, so markers never affect filtering or previous-selection identity.

The merge chooser carries `IsCheckedOutInLinkedWorktree` through `GitMergeBranchOptionItem`, initial construction, selected/unselected lists, and every rebuild. Both chooser panels render `+` for linked occupancy, while `FilterValue`, membership checks, and selected-name extraction use only canonical `BranchName`. Rebuilds continue to walk `AllBranches()` and therefore preserve discovery order rather than click order.

### Merge command boundary

Extract one merge-argument builder used by both `GitMerge` and `GitMergeWithSigning`:

```text
FfMerge=true:  merge --ff    -- <branch...>
FfMerge=false: merge --no-ff -- <branch...>
```

The `--` belongs to `api/git`, after all merge options and before the first ref operand. TUI and service callers continue to pass only canonical names; they do not trim `+`, qualify refs, sanitize display strings, or insert their own terminator.

This preserves the current namespace-relative contract and stable discovery-order multi-selection. It also closes an option-injection boundary for refs such as `-foo` that can be created through plumbing even when porcelain branch creation rejects them. Git still resolves execution-time deletion races, same-named tags, conflicts, cancellation, and signing interaction exactly as before.

### Compatibility, failure, and rollback

The required `for-each-ref` atoms and format conditionals predate the documented Git 2.36+ minimum. There is no persisted data or migration. The first successful refresh after rollout replaces any cached decorated identity with canonical data; failed refreshes retain only an in-memory last-good generation.

Rollback is a code revert with no data cleanup. The change performs no worktree mutation during discovery and exposes no worktree path, reducing both framing and privacy surface.

## 3. Implementation

### Phase 1: Publish and render canonical local-branch snapshots

**ID:** `1`
**Goal:** every supported HEAD state exposes one complete canonical local-branch generation, and the local panel renders current/linked status without mixing decoration into identity
**Tests:** `api/git/branch_test.go`, `api/git/local_refs_test.go`, `tui/component/branch/types_test.go`

**Acceptance criteria:**

- [ ] A stateless `api/git` helper runs `for-each-ref` over `refs/heads/` and returns every namespace-relative local branch with separate current and occupied booleans.
- [ ] The wire format is exactly `name NUL current NUL occupied LF`; the parser accepts valid empty and multi-record snapshots and rejects empty names, missing or extra fields, unknown booleans, partial records, extra terminal records, and multiple current records.
- [ ] A literal branch name beginning with `+` survives parsing unchanged, and worktree paths never enter parser output or `BranchInfo`.
- [ ] `BranchInfo.IsCheckedOut` means only the current worktree's HEAD; `IsCheckedOutInLinkedWorktree` is true only for an occupied non-current ref.
- [ ] `GitBranch` publishes local state in one synchronized/atomic replacement, and readers cannot mutate the stored slice through returned values.
- [ ] An execution, parse, or non-classifying symbolic-ref failure preserves the complete last-good snapshot; no pre-publication field reset leaks to readers.
- [ ] Attached, fresh-unborn, orphan-unborn-with-existing-refs, and detached states publish according to the classification table, including attached-to-detached and detached-to-attached transitions.
- [ ] Detached state clears stale current data, retains discovered refs, sets `m.CheckOutBranch` empty, and emits no blank local-panel row.
- [ ] `AllBranches()` continues to exclude the current branch and include linked-worktree branches in deterministic discovery order.
- [ ] `InitBranchList` reads one generation, prepends current only when present, applies the `+1` selection offset only for that row, and never duplicates current.
- [ ] The local panel renders `*` for current and `+` for linked occupancy, while `FilterValue`, selection restoration, switching, deletion, and other identity comparisons use only canonical names.
- [ ] Commit-log refresh invokes the shared stateless discovery independently, preserves its clear-on-enumeration-failure policy, and retains existing commit-log publication behavior.
- [ ] Repository tests cover linked occupancy, unusual worktree paths, canonical names, synchronized readers, failed-refresh retention, and all listed HEAD transitions. Tests that replace the global executor do not call `t.Parallel()`.

**Steps:**

1. In `api/git/branch.go` (or a focused local-ref helper file), define the strict record parser, stateless discovery command, linked-status projection, and immutable local snapshot.
2. Initialize and atomically replace the snapshot in `GitBranch`; adapt compatibility accessors and add a one-generation accessor for combined current/other reads.
3. Implement strict symbolic-HEAD classification, distinguishing the documented detached exit from all other failures before publication.
4. Replace `commitlog.go`'s name-only command with an independent invocation of the shared helper and preserve its existing error policy.
5. Extend `GitBranchItem` and update `tui/component/branch/init.go` and `types.go` for conditional current insertion, correct offsets, canonical filtering, and `*`/`+` rendering.
6. Add parser, temporary-repository, transition, failure-publication, concurrency, branch-list projection, rendering, filtering, and selection-offset tests in the relevant `api/git` and `tui/component/branch` packages.

### Phase 2: Merge linked-worktree branches safely

**ID:** `2`
**Goal:** the merge chooser keeps linked occupancy visible while both background and signing merges receive unchanged canonical refs behind an option terminator
**Tests:** `api/git/merge_test.go`, `tui/popup/branch/merge_test.go`

**Acceptance criteria:**

- [ ] `GitMergeBranchOptionItem` carries linked-worktree status from `BranchInfo` during initial chooser construction.
- [ ] Select and unselect rebuilds preserve or refresh that status in both chooser panels without adding the marker to `BranchName`.
- [ ] Both available and selected chooser rows render `+` for linked branches; filtering, equality, membership, and selected-name extraction remain canonical-name-only.
- [ ] Linked-worktree branches remain selectable and are not treated as the current checkout.
- [ ] One merge-argument builder is used by background and signing paths and emits `merge --ff -- <refs...>` or `merge --no-ff -- <refs...>`.
- [ ] A canonical linked branch merges successfully through the background path, and the signing path returns the same operand boundary without executing in-process.
- [ ] Literal `+skill` reaches Git unchanged; no layer trims or converts it as presentation text.
- [ ] An option-shaped ref is placed after `--` and is not parsed as an option.
- [ ] Multi-branch operands follow discovery/`AllBranches()` order after chooser rebuilds, not click order, for both FF modes.
- [ ] Existing locking, cancellation, signing suspension, output, conflict, and logging behavior is unchanged.
- [ ] Focused tests cover initial and rebuilt chooser decoration, canonical filtering/equality, both FF modes, background and signing paths, current exclusion, literal-plus and option-shaped names, and stable multi-selection order. Tests sharing the global executor do not run in parallel.

**Steps:**

1. Add linked status to `GitMergeBranchOptionItem` and populate it in `tui/popup/branch/init.go`.
2. Rework `UpdateChooseBranchOptionForMergePopUpModel` to rebuild typed items from the current canonical `AllBranches()` generation while retaining selected membership by name and discovery order.
3. Update the chooser delegate to render `+` from status while leaving `FilterValue()` unchanged.
4. Extract and use a single merge-argument builder in `api/git/branch.go`, placing `--` after strategy options and before all operands.
5. Add chooser model/delegate tests, pure argv tests, and temporary-repository merge tests for both execution routes and hostile-but-valid ref names.

### Phase 3: Full test sweep

**ID:** `3`
**Goal:** every test declared by this spec is green together
**Tests:** all

**Acceptance criteria:**

- [ ] The union of test paths declared by completed phases passes through the scoped resolver.
- [ ] Failures surfaced by the sweep are remediated in this phase.
- [ ] Any test authored during remediation is appended after `all` on this phase's `Tests` line before its gate reruns.
- [ ] This phase introduces no lint work of its own beyond the usual lint and review required for a remediation fix.

### Phase 4: Outcome

**ID:** `4`
**Goal:** reconcile delivered behavior and evidence against this design
**Tests:** none — this reconciliation phase authors no tests

**Acceptance criteria:**

- [ ] Delivered behavior, deviations, settled decisions, deferred work, advisory coverage gaps, and the full-sweep result are summarized briefly in `Outcome`.
- [ ] All Open Questions are cleared before this phase completes.

**Steps:**

1. Compare the completed phases and verification evidence with the design and record the final result in `Outcome`.

### Phase 5: Documentation

**ID:** `5`
**Goal:** document the shipped branch markers, supported HEAD behavior, and safe merge boundary in the repository's established documentation system
**Tests:** none — this documentation-only phase authors no tests

**Acceptance criteria:**

- [ ] `$ckit:docs` is invoked through its build-owned route for shipped behavior and verification instructions.
- [ ] Documentation placement is discovered when this phase runs and reconciles the existing documentation system; if none exists, it uses the nearest relevant README and then the root README.
- [ ] User-facing documentation explains that `*` means the current worktree, `+` means another linked worktree, and neither marker is part of the branch name.
- [ ] Documentation does not promise richer detached-HEAD labels, click-order merges, or fully qualified merge operands.

**Steps:**

1. Discover the documentation target, invoke `$ckit:docs`, and reconcile the resulting user and verification guidance with shipped behavior.

## 4. Verification

- [ ] In a temporary repository with `main`, `feature`, and a linked worktree checking out `feature`, the local panel shows `* main` and `+ feature`, with each item's `FilterValue()` equal to the undecorated name.
- [ ] Selecting `feature` in the merge chooser retains `+` through select/unselect rebuilds and executes Git with `feature`, not `+ feature`.
- [ ] Branches named `+skill` and option-shaped refs created through plumbing are passed byte-for-byte after `--` in both FF modes and both merge paths.
- [ ] A multi-branch merge uses discovery order after chooser rebuilds and excludes only the current worktree's branch.
- [ ] A fresh repository, an orphan branch coexisting with committed refs, and a populated repository transitioning attached → detached → attached show no stale current branch, duplicate current row, or detached blank row.
- [ ] A malformed discovery record, failed `for-each-ref`, and unexpected symbolic-ref error each retain the prior complete branch snapshot.
- [ ] Concurrent snapshot reads observe only complete generations under the race detector or an equivalent synchronization-focused test.
- [ ] Commit-log local-name capture still returns canonical names independently and still clears its own local-name list after enumeration failure.
- [ ] Existing merge cancellation, signing suspension, conflict reporting, branch switching/deletion, and commit-log tests remain green.

## 5. Open Questions

No unresolved design questions remain. Richer detached-HEAD presentation, click-order merge semantics, merge-strategy redesign, and fully qualified local-ref operands are explicit follow-up work rather than blockers.

## Outcome

<!-- Build fills this after implementation, including the full-sweep result. Keep it brief. -->
