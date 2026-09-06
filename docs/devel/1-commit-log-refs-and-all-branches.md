---
status: in-progress
issue: 1
pr: null
completed: [1]
---

# Commit Log Ref Decorations and Optional All-Branches History — Design Document

**Derived from:** `.ckit/scratch/commit-log-refs-and-all-branches.md`

Gitti's Commit Log panel draws commit topology but discards Git's ref decorations, so
no lane in the graph can be tied to a branch, a tag, or `HEAD`. The panel also walks
only the checked-out history, so commits on other branches are absent entirely. This
change attaches ref names to every commit row and adds an opt-in mode that walks all
local branches, remote-tracking branches, and tags, while leaving the existing lane
renderer untouched. It is scoped to be reviewable as an upstream pull request.

## 1. Motivation

### Current state

`GetCommitLogs` (`api/git/commitlog.go:82-138`) runs a single fixed query:

```go
"log", "--topo-order", "--no-decorate", "--no-notes",
"--pretty=format:%H%x00%P%x00%s%x00%an",
"-n", gCL.maxCommitLogCount, "--",
```

Four NUL-separated fields are split with `SplitN(line, SEPARATOR, 4)` and fed to
`GraphRenderer.RenderCommit`, which produces the lane cells. The row delegate
(`tui/component/commitlog/types.go:76-83`) composes:

```
hash(7) | author-monogram(3) | graph-lane | subject
```

Two consequences:

- No ref information reaches the model at all, so a lane is anonymous. A user looking
  at the graph cannot tell which lane is `main`, which is their feature branch, or
  where `HEAD` sits.
- Because the query walks `HEAD` only, a branch that has diverged and is not an
  ancestor of the current checkout contributes no rows. The graph shows a single
  strand of history and cannot show the fork.

### Goal

Ref decorations (`HEAD -> branch`, `branch`, `origin/branch`, `tag: v0.9.0`) appear
next to the commit they point at, in both modes, and participate in the panel filter
so a user can type a branch name to isolate its commits. A new opt-in setting widens
the walk to all branches, remote-tracking branches and tags, so diverged work appears
as real lanes in the existing graph.

**Non-goals:**

- Rewriting `GraphRenderer` or its lane/colour-identity model.
- Recovering the names of deleted branches. Git does not retain them; a merge subject
  is not a ref.
- A runtime in-app toggle between modes. The setting takes effect on restart.
- Separate commit-count limits per mode.
- Reworking configuration handling generally. The `ensureConfigIntegrity` boolean defect
  **is** in scope and is Phase 1, because a `commit_log_show_refs` setting that defaults
  to `true` is unusable without it (see Design, "Configuration"). The repair is scoped to
  booleans only; `String`, `Int` and `Float64` handling is untouched.

### Use cases

- A developer scanning the Commit Log sees `[HEAD -> feature/x]` on their tip and
  `[main, origin/main]` on the merge base, and can read the graph without leaving
  Gitti for `git log --graph`.
- A developer enables all-branches mode and sees a colleague's fetched branch as a
  labelled lane diverging from `main`, instead of nothing at all.
- A developer types `F` and a branch name to filter the Commit Log down to the
  commits carrying that ref, even when no subject mentions it.

## 2. Design

### Git query

Two argument groups are added to `GetCommitLogs`.

**Deterministic decorations (both modes, always).** `%D` alone is *not* a stable ref
set: `log.excludeDecoration` strips refs from `%D` even under `--no-decorate`, and
`log.initialDecorationSet=all` injects `refs/stash` and `refs/notes/*` into it. Both
were reproduced against git 2.55. Explicit `--decorate-refs` arguments override both,
so Gitti pins the decoration set rather than inheriting the user's:

```
--decorate-refs=HEAD
--decorate-refs=refs/heads/*
--decorate-refs=refs/remotes/*
--decorate-refs=refs/tags/*
--decorate-refs-exclude=refs/remotes/*/HEAD
```

The exclusion drops `origin/HEAD`, which otherwise renders as a third redundant label
on the default branch tip. `--no-decorate` stays because it is already there and is
harmless: a user format containing `%D` re-enables decoration loading regardless, so
`--no-decorate` neither suppresses nor enables anything here.

**All-branches walk (opt-in only).** `--all` is wrong here. It walks `refs/stash` and
`refs/notes/*`, injecting `WIP on …`, `index on …` and `Notes added by …` rows, and the
stash commit's synthetic second parent draws as a real fork in the lane graph.
`--no-notes` does not prevent the walk. `--all` also covers `refs/bisect/*`,
`refs/rewritten/*` — live during Gitti's own interactive-rebase feature —
`refs/worktree/*` and fetched `refs/pull/*`. The mode instead appends:

```
--branches --remotes --tags --ignore-missing HEAD
```

`--ignore-missing` is load-bearing. Without it, `HEAD` on an unborn branch (after
`git switch --orphan`, or in a fresh repository) makes git exit `fatal: bad revision
'HEAD'` and the entire log is lost even though every branch is intact. Gitti terminates
its argument list with `--` (`api/git/commitlog.go:91`), and once git has seen a `--` a
failed revision argument is fatal rather than reinterpreted as a pathspec, so the
failure is unconditional. It is also silent: `GetCommitLogs` never calls `cmd.Wait()`
and never inspects the exit status, so nothing is logged. With `--ignore-missing` the
unborn case degrades to the branch set, the empty repository yields a clean empty
result, and a detached `HEAD` is still included and still decorated `HEAD`. All three
were reproduced against git 2.55 with the trailing `--` present.

The new arguments are inserted **before** the trailing `--`. `HEAD` placed after it
would be parsed as a pathspec.

`HEAD` is still needed alongside `--branches` because a detached `HEAD` matches no
branch ref. A *detached* `HEAD` in another worktree is not walked; that is a non-goal.
Branches checked out in other worktrees are covered by `--branches`.

The pretty format gains `%D` in third position:

```
--pretty=format:%H%x00%P%x00%D%x00%s%x00%an
```

`%D` emits its NUL separator even when empty, so the field count is a stable five.

**Scanner ceiling and process reaping.** `GetCommitLogs` reads through `bufio.Scanner`
without calling `Buffer` and without checking `Err()` (`api/git/commitlog.go:107-137`),
and never calls `cmd.Wait()` after `cmd.Start()` (`:102-105`). A commit carrying 900
tags produced a 38 KB `%D` field against `bufio.Scanner`'s 64 KB default cap; past that
cap `Scan()` returns false with `ErrTooLong`, the scan ends silently, and every
remaining commit is dropped with no error surfaced. Because the read end of the pipe is
then never drained or closed, git blocks on a full pipe — one hung `git log` per daemon
refresh (`api/daemon.go:296-302`). A tip decorated by well over a thousand remote
branches is enough to reach this in a large monorepo, and adding `%D` is what makes it
reachable at all.

The phase that adds `%D` therefore also sets an explicit buffer ceiling
(`scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)`) and logs `scanner.Err()` through the
existing `logging.COMMIT_LOG_OPS` channel. The missing `Wait()` is pre-existing, but this
change is what makes the failure mode plausible, so it is repaired here rather than
deferred.

**Adding `cmd.Wait()` alone would make things worse, not better.** `StdoutPipe`'s
contract is that `Wait` closes the pipe on process exit, so it is invalid to call `Wait`
before all reads have completed. On the `ErrTooLong` path the reads have *not* completed:
the scanner stops unrecoverably while git may still be blocked writing into a full pipe.
`Wait` would then block forever, and because the daemon guards this call with
`isGitCommitLogPassiveRunning.CompareAndSwap` (`api/daemon.go:296-302`) the flag would
never be cleared and the commit log would never refresh again — a permanently dead panel
rather than today's leaked process.

The teardown contract is therefore explicit:

1. after the read loop, capture `scanner.Err()`;
2. on a non-nil error, log it and kill the child (`git log` is read-only, so killing is
   safe);
3. drain whatever remains on the pipe to `io.Discard`, so a still-writing git unblocks
   and cannot wedge step 4;
4. `cmd.Wait()`, and log a non-nil wait error.

Steps 3 and 4 run on the success path too, where the drain is a no-op.

### Data flow

```
GetCommitLogs
  buildCommitLogArgs(maxCount, allBranches) → []string
  ↓ git log
  parseCommitLogLine(line) → (CommitLog{Hash,Parents,Refs,Message,Author}, ok)
  ↓ GraphRenderer.RenderCommit (unchanged)
CommitLog.Refs
  ↓ InitGitCommitLogList
GitCommitLogItem.Refs → FilterValue + row render
  ↓ action handlers
reset confirmation popup · cherry-pick popup
```

`buildCommitLogArgs` and `parseCommitLogLine` are extracted as pure functions out of
`GetCommitLogs`. This is the only structural change to that function and it exists so
the repository's first tests can cover the argument set and the parser without a Git
fixture or a running TUI. Streaming behaviour is preserved: the scanner loop still
calls the parser per line.

### Rendering

Refs render inline, immediately after the graph lane and before the subject, wrapped in
`[...]` and styled bold in the row's lane colour. `%D` is rendered verbatim: no
re-parsing into per-ref-type styling in this change.

There is deliberately **no fixed refs column**. The default `left_panel_width_ratio` is
`0.3` (`settings/settings.go:50`) and `ListItemOrTitleWidthPad` is `4`
(`tui/constant/constant.go:96`), so on a 200-column terminal the panel has roughly 56
usable columns. A fixed column would surrender that width on every row when most rows
carry no refs at all.

For the same reason the ref block is width-capped. `[HEAD -> refactor/70-strip-nightshift]`
is 38 columns on its own, and the delegate currently truncates the whole composed line
with `MaxWidth` plus a literal `"..."` (`tui/component/commitlog/types.go:87-93`), so an
uncapped ref block silently evicts the subject and leaves the row less informative than
before the change.

The cap is defined against the width *remaining after the lane*, not against the panel
width, because the lane block is itself uncapped: it grows at two columns per concurrent
lane (`api/git/commitlog.go:315`) and all-branches mode is exactly what multiplies
concurrent lanes. On the default 56-column panel, hash, monogram and separators already
take 13 columns, so roughly twenty concurrent lanes evict the subject before a single
ref is drawn. A cap expressed as a share of panel width would not bound anything.

Concretely, with `avail = componentWidth - (7 + 1 + 3 + 1 + laneWidth + 1)`:

- `avail < 30` → the ref block is omitted entirely; refs collapse to zero rather than
  competing with the subject on a narrow row.
- otherwise the block, brackets included, is truncated to at most `avail / 2`.

The subject therefore retains at least 15 columns on any row that shows refs. Truncation
uses `ansi.Truncate` on the raw string *before* styling — `github.com/charmbracelet/x/ansi`
is already a direct dependency (`go.mod:10`) and already imported in this package
(`tui/component/commitlog/init.go:5`) — so the existing `MaxWidth` pass on the composed
line stays ANSI-safe. A commit with no refs emits nothing, not an empty `[]`.

### Filtering

`Refs` joins `FilterValue()`, which is the mechanism that makes branch-name filtering
work. Accepted cost: `utils.FilterListItems` (`tui/utils/utils.go:100-104`) restores the
previously selected item by `FilterValue()` string equality, so while a filter is
active a selection can be lost if a ref moves onto or off the selected commit between
refreshes. The unfiltered path matches by hash and is unaffected.

### Configuration

Two settings, both booleans:

| JSON key | Go field | Default | What it changes | What it does **not** change |
| --- | --- | --- | --- | --- |
| `commit_log_show_refs` | `CommitLogShowRefs` | `true` | Ref labels on Commit Log rows, and whether the panel filter matches ref text | Which commits are loaded; every popup; every action |
| `commit_log_show_all_branches` | `CommitLogShowAllBranches` | `false` | Which commits the Commit Log loads, hence its rows and graph lanes | How a row is rendered; every action |

The shared `commit_log_` prefix is the scope statement: both settings are properties of
the Commit Log view and of nothing else. Past that prefix each name says what it does to
that view — one shows ref labels, the other shows commits from all branches — and the
parallel `show_` verb keeps them reading as two facets of the same panel rather than two
unrelated levers.

Neither setting changes what an action does once a commit is chosen: a reset resets, a
revert reverts, and a cherry-pick applies the same hashes, identically, in every
combination of the two.

**The cherry-pick picker follows the Commit Log by construction, and that is in scope
rather than a leak.** `InitGitCherryPickPopUpModel` builds its list from
`m.CurrentRepoCommitLogInfoList.Items()` (`tui/popup/commitlog/init.go:74-90`) and is only
ever opened from the Commit Log panel against the checked-out branch
(`ctrl_p.go:30,41`, `enter.go:390`, all passing `m.CheckOutBranch`). It is a selection
view *over* the Commit Log, not an independent surface, so widening the Commit Log
necessarily widens it — the same way it already follows `max_commit_log_count`. What must
not survive that is the popup's claim that every listed commit came from the current
branch, which stops being true once the log is wider; Phase 4 makes the label truthful.
No other popup or action reads the Commit Log list.

Ref display is on by default. This is a settled decision, not a leftover: it is the point
of the change, and a feature nobody sees unless they find a flag is a weaker proposition
upstream. It is nonetheless a genuine preference — refs consume scarce panel width — so a
user who prefers today's denser rows must be able to turn it off, which is what makes the
Phase 1 repair load-bearing rather than optional.

**That requires repairing boolean persistence first.** `ensureConfigIntegrity`
(`settings/settings.go:150-184`) switches on `reflect.String`, `Int`, `Int64` and
`Float64` and routes everything else to a `default:` branch that resets any zero value
to the default:

```go
default:
    if reflect.DeepEqual(field.Interface(), reflect.Zero(field.Type()).Interface()) {
        field.Set(defaultField)
        changed = true
    }
```

`bool` lands there, so a persisted `false` is indistinguishable from unset and is
rewritten to the default on every launch. Any setting defaulting to `true` is therefore
impossible to turn off. This is already live for `auto_update` and
`allow_commit_graph_write` (`settings/settings.go:54,58`): `gitti --auto-update false`
writes `false`, and the next launch rewrites it to `true`.

The repair is presence-detection rather than zero-detection, for booleans only:

```go
// A mirror of the struct whose bools are pointers; nil means the key was absent or null.
mirror := reflect.New(reflect.StructOf(mirroredFields))
json.Unmarshal(data, mirror.Interface())
...
case reflect.Bool:
    if declared == nil {
        field.Set(defaultField)
        changed = true
        continue
    }
    field.SetBool(*declared)     // an explicit false is now preserved
```

The mirror carries the same `json` tags, so `encoding/json` — not hand-written key
matching — decides what each bool field was given. A missing key still takes the default
and still triggers a rewrite, so an existing config file gains the new keys on first
launch after upgrade.

Presence-detection is applied **only** to booleans. The `String`, `Int` and `Float64`
branches keep zero-detection, because for those fields the zero value is not a legitimate
setting and the `Int` branch itself is the fallback: an explicit
`max_commit_log_count: 0` must resolve to the default via `settings/settings.go:165-169`,
and `main.go:65,93` likewise treats `0` as "flag not supplied". Switching those to
presence-detection would make `0` a honoured value. `LastUpdateCheckTime` is a
`time.Time` and continues to fall through to `default:` unchanged.

**Upgrade impact — the opposite of what it first looks like.** The intuitive reading is
that a user who previously set `auto_update: false` will finally have it honoured. That
is wrong, because the current code does not merely override the value in memory: it
writes the override back to disk. `ensureConfigIntegrity` sets `changed`, and
`InitOrReadConfig` then calls `saveConfig` (`settings/settings.go:115-118`).

`changed` is moreover true on *every* launch regardless of the user's file, because
`ff_merge` and `override_signing_ui_suspend` default to `false`, and a default-`false`
bool is itself zero-valued and so always "reset" (`settings/settings.go:61-62,175-181`).
The config file is therefore rewritten on every single launch.

Consequence: anyone who ran `gitti --auto-update false` and then started Gitti even once
already has `auto_update: true` on disk. Presence-detection will read that `true` and
nothing changes for them. The only affected files are those that still literally contain
`false` — the flag was set but Gitti has not been launched since, or the file was
hand-edited.

So the release note must not say "your setting now takes effect". It must say the
setting was silently overwritten on earlier versions and needs re-applying:
*if you previously set `--auto-update false` or `--allow-commit-graph-write false`,
re-run the flag after upgrading.*

### Scope of the ref-display setting

`commit_log_show_refs` gates the Commit Log row and nothing else: when off, the row
renders exactly as it does today, and `FilterValue()` excludes refs so a filter cannot
match text the user cannot see.

The row render and `FilterValue()` both read `settings.GITTICONFIGSETTINGS` directly,
which is the established pattern for this global (`api/utils.go:97`, `main.go:119`,
`config/config.go`).

Reading it from the render path is race-free, and deliberately so. The global is written
in exactly two places: `InitOrReadConfig` at startup (`settings/settings.go:88,142`), and
the `Update*` setters (`:218-367`). Every setter is reachable only from a `config.Set*`
function that calls `os.Exit(0)` immediately afterwards, so those paths never coexist
with a running TUI. The one non-CLI writer, `UpdateLastFetchTime` via
`updater.AutoUpdater()`, runs synchronously at `main.go:126` — before `tea.NewProgram`
and before the daemon's goroutines exist. The global is therefore effectively immutable
for the lifetime of the TUI, and no synchronization is introduced. Any future setter
reachable while the TUI is live would invalidate this and needs guarding.

It does **not** gate the action popups from the Actions section below, and this is a
settled decision rather than an open one. A display preference must not silently reach
into a confirmation dialog: the refs shown before a reset or a revert are a safety
affordance for a mode the user opted into, not decoration, and the popups are not
width-starved the way a 56-column panel is. Rather than widen the setting to cover them,
the setting is *named* for the surface it governs, so its limit is visible at the point
of use.

`%D` is fetched unconditionally, in both settings' off states. Gating the pretty format
on the setting would mean two formats and two parsers for a negligible saving, and the
popups need the data regardless.

### CLI surface

The CLI follows the established convention exactly: every flag in `main.go:58-106`
writes the setting, prints a confirmation and calls `os.Exit(0)`, and boolean settings
use `flag.String` with `"true"`/`"false"` parsing. So both new flags persist and exit;
neither is a session-scoped override:

```
gitti --commit-log-show-refs true|false
gitti --commit-log-show-all-branches true|false
```

Eight i18n strings are needed across all four locales — a flag description and three
status strings per setting.

### Actions on commits outside the current branch

All-branches mode's entire effect is to widen the set of commits the panel's actions can
target, so the change carries one explicit rule rather than a documentation note:

> A commit-targeting action must show the refs of the commit it will act on.

- **Reset** (`tui/interaction/handler/nontyping/r.go:63-77`) opens a confirmation
  carrying hash, subject and author. Under all-branches a user can reset `HEAD` onto an
  unrelated branch's commit — valid Git, already gated by the confirmation, but the
  popup shows no ref. It gains a refs line beside the hash
  (`tui/popup/commit/render.go:249-265`). This applies in both modes; it is strictly
  more information.
- **Cherry-pick** (`tui/popup/commitlog/init.go:74-90`) builds its list from the panel
  and stamps every entry `FromBranch: <current branch>`. `FromBranch` is display-only —
  rendered as "Cherry picked from branch ~>" at `tui/popup/commitlog/types.go:162` —
  and the operation itself passes hashes (`api/git/commitlog.go:478-489`), so it stays
  correct while the label becomes false. Under all-branches the label is sourced from
  the commit's own refs, and the delegate omits the line entirely when that is empty
  rather than asserting a provenance it does not have. Current-branch mode is
  byte-identical to today.
- **Revert** (`tui/interaction/handler/nontyping/ctrl_r.go:27-48`) and **create tag**
  (`tui/interaction/handler/nontyping/t.go:24-28`) take the same panel hash and gain the
  same reach. Both show the target's refs under the rule above.
- **Reset-latest** (`tui/interaction/handler/nontyping/shift_r.go:25`) gates on
  `len(m.CurrentRepoCommitLogInfoList.Items()) > 1` as a proxy for "`HEAD` has more than
  one commit". All-branches mode breaks the proxy: the gate passes when `HEAD` has a
  single commit but other branches contribute rows, and the subsequent `HEAD~1` reset
  then fails. The gate must not be derived from the panel list.
- **Interactive rebase** is structurally unaffected: it runs its own `HEAD`-only query
  (`api/git/interactive-rebase.go:60-71`) rather than reading the panel list.

Note the mode split precisely, because it differs per popup. The reset, revert and tag
popups show refs in **both** modes — that is a deliberate, visible change against today
for any commit that carries refs, not a no-op. Only the cherry-pick popup is
mode-conditional, because only it currently asserts a provenance that all-branches mode
falsifies.

### Graph renderer

Untouched, by design. `RenderCommit` assigns `ColorID` from lane *position* —
`len(g.currentLanes)` for a new tip (`api/git/commitlog.go:210-217`), `len(nextLanes)`
for a fork (`:294-297`) — and resolves a pass-through lane's next column by matching
`ColorID` (`:378-384`). Because colour IDs are recycled positions rather than stable
identities, concurrent lanes can collide, and all-branches mode raises the number of
concurrent lanes and therefore the collision rate. Layer 4 also colours the node with
`commitLaneIdx` rather than `commitLane.ColorID` (`:440`), inconsistent with every other
layer. Both are pre-existing. This change verifies the renderer's output visually
instead of altering it; colour-identity cleanup is separate upstream work.

### Commit limit

`-n <max_commit_log_count>` continues to apply across the union in all-branches mode, so
an active repository may push older current-branch commits out of view. Documented, not
solved: the opt-in default keeps existing users on existing behaviour. Verification
covers the truncated multi-tip case explicitly, because a limit applied across several
tips leaves lanes open at the truncation boundary in a way single-strand history does
not.

### Baseline

Go 1.27.1 is installed (`/opt/homebrew/bin/go`), satisfying `go.mod`'s `go 1.25.7` and
matching CI, which pins `go-version: stable` (`.github/workflows/release.yml:27-29`).

The pre-change tree is verified green: `go vet ./...` clean, `go test ./...` passing with
no test files, `go build -o ./bin/gitti .` producing a working `v0.9.0` binary, and every
file this spec touches already `gofmt`-clean. Any gate failure during implementation is
therefore attributable to the change rather than to pre-existing state.

The repository contains zero `*_test.go` files, so this change introduces its first
tests. The pre-commit configuration (`.pre-commit-config.yaml`) already wires `go fmt`,
`goimports`, `go vet` and `go mod tidy` as `language: system` hooks, so they run against
this host toolchain on every commit.

## 3. Implementation

### Phase 1: Repair boolean settings persistence

**ID:** `1`
**Goal:** a boolean setting written as `false` survives a restart, so a setting that
defaults to `true` can be turned off at all
**Tests:** `settings/settings_test.go`

This phase is a standalone bug fix with no dependency on the rest of the spec. It ships
first because every later phase's default-`true` ref toggle is unusable without it, and
it is independently valuable: it is the reason `gitti --auto-update false` does not
currently stick.

**Acceptance criteria:**

- [ ] `ensureConfigIntegrity` decides boolean fields by key presence in the raw JSON, not
      by zero value; the key name comes from the field's `json` tag with any `,option`
      suffix stripped.
- [ ] A boolean key present with an explicit `false` is preserved across a restart.
- [ ] A boolean key that is absent, or present as `null`, takes its default and marks the
      config changed, so an upgraded config file gains the new keys on first launch.
- [ ] The key derivation falls back to the Go field name when the `json` tag is empty
      and skips `json:"-"`, so a future untagged boolean is not reset on every launch.
- [ ] `String`, `Int`, `Int64` and `Float64` fields keep zero-detection unchanged: an
      explicit `max_commit_log_count: 0` still falls back to the default.
- [ ] A config file containing every key is no longer rewritten on launch. Today
      `saveConfig` runs every time, because the default-`false` `ff_merge` and
      `override_signing_ui_suspend` always trip the reset; after this phase the file is
      written only when a key is genuinely missing or `null`.
- [ ] `LastUpdateCheckTime` still round-trips through the `default:` branch unchanged.
- [ ] `gitti --auto-update false` and `gitti --allow-commit-graph-write false` both
      survive a restart.

**Steps:**

1. In `InitOrReadConfig`, decode the file a second time through `decodeDeclaredBools`,
   which mirrors `GittiConfigSettings` with `*bool` fields so `encoding/json` itself
   reports which bools the file declares, and pass the result to `ensureConfigIntegrity`.
2. Add a `case reflect.Bool:` branch ahead of `default:` that takes the decoder's answer:
   a nil pointer means absent or null and takes the default; otherwise the declared value
   is written to the field.
3. Write `settings/settings_test.go` covering: explicit `false` preserved, absent key
   defaulted, `null` treated as absent, explicit zero int still defaulted, `time.Time`
   untouched, a complete config reporting no change, a case-variant key the decoder
   honours, duplicate-key resolution agreeing with the typed decode, and an
   `InitOrReadConfig` round trip proving a disabled bool survives a restart.

Note for the implementer: do not hand-roll the key matching. `encoding/json` resolves
case-variant spellings, duplicate keys and `null` by rules a raw `map[string]json.RawMessage`
lookup does not reproduce, and any divergence between the two decodes rewrites the file with
a value the user never wrote. Decoding into the mirror struct keeps one decoder in charge of
both answers, so the question cannot arise.

### Phase 2: Show ref decorations on commit log rows

**ID:** `2`
**Goal:** every commit row displays the refs pointing at it, the panel filter matches
them, and a user who prefers today's denser rows can turn the whole thing off
**Tests:** pending

**Acceptance criteria:**

- [ ] `CommitLog` carries a `Refs string` field populated from `%D`.
- [ ] The query uses `--pretty=format:%H%x00%P%x00%D%x00%s%x00%an` and parses five
      NUL-separated fields, skipping lines with fewer.
- [ ] The query pins its decoration set with `--decorate-refs` for `HEAD`,
      `refs/heads/*`, `refs/remotes/*` and `refs/tags/*`, and excludes
      `refs/remotes/*/HEAD`; refs still render correctly with
      `log.excludeDecoration` or `log.initialDecorationSet=all` set in user config.
- [ ] `buildCommitLogArgs` and `parseCommitLogLine` exist as pure, separately testable
      functions; `GetCommitLogs` still streams line by line.
- [ ] The scanner has an explicit buffer ceiling and `scanner.Err()` is logged through
      `logging.COMMIT_LOG_OPS`.
- [ ] Teardown follows the kill/drain/wait contract: an over-long line leaves no blocked
      `git log` process **and** does not block `GetCommitLogs`, so the daemon's
      `isGitCommitLogPassiveRunning` flag is always cleared and the panel keeps
      refreshing.
- [ ] `GitCommitLogItem` carries `Refs`, populated on every construction path in
      `tui/component/commitlog/init.go` (there are two).
- [ ] `FilterValue()` includes `Refs`, so filtering by a branch name matches commits
      whose subject does not contain it.
- [ ] The row renders `[refs]` between the graph lane and the subject, bold in the
      row's lane colour, and omits the block entirely when the commit has no refs.
- [ ] The ref block is omitted when fewer than 30 columns remain after the hash,
      monogram and lane, and is otherwise truncated to at most half of that remaining
      width, so the subject retains at least 15 columns on any row showing refs.
- [ ] A detached `HEAD` renders its `HEAD` decoration without assuming a branch name.
- [ ] `CommitLogShowRefs` exists as `commit_log_show_refs`, defaults to `true`, and
      survives a restart when set to `false` (depends on Phase 1).
- [ ] With the setting off, the row renders exactly as it does today and `FilterValue()`
      excludes refs, so a filter cannot match text the user cannot see.
- [ ] `gitti --commit-log-show-refs true|false` persists the setting, prints a localized
      confirmation and exits, rejecting any other value.
- [ ] The flag and its three status strings are present in `en`, `ja`, `zh-hans` and
      `zh-hant`.

**Steps:**

1. Add `Refs` to `CommitLog` in `api/git/commitlog.go`.
2. Extract `buildCommitLogArgs(maxCount string, allBranches bool) []string` with
   `allBranches` wired but always `false` at this phase, and
   `parseCommitLogLine(line string) (CommitLog, bool)`.
3. Add the `%D` field and the `--decorate-refs` group; harden the scanner.
4. Add `Refs` to `GitCommitLogItem` and to `FilterValue`; populate both construction
   paths in `tui/component/commitlog/init.go`.
5. Render the capped ref block in the delegate in `tui/component/commitlog/types.go`.
6. Add `CommitLogShowRefs` to `settings/settings.go` with an `UpdateCommitLogShowRefs`
   setter, `config.SetCommitLogShowRefs` following `config.SetFfMerge`, the flag and its
   `switch` case in `main.go`, and `FlagCommitLogShowRefs`,
   `CommitLogShowRefsEnabled`, `CommitLogShowRefsDisabled` and
   `CommitLogShowRefsSetError` across `i18n/types.go` and all four locale files.
7. Gate the row render and `FilterValue()` on the setting.
8. Extract the teardown as
   `drainAndWait(cmd *exec.Cmd, stdout io.ReadCloser, scanErr error) error` so the
   contract is testable without going through `executor.GittiCmdExecutor`, which
   hardcodes the `git` binary and offers no seam.
9. Write `api/git/commitlog_test.go` covering:
   - the argument set for both modes;
   - the parser — five-field lines, an empty `%D`, a root commit with no parents, a
     multi-parent commit, and a short malformed line;
   - the read loop against an `io.Reader` whose line exceeds the ceiling, asserting
     `ErrTooLong` is surfaced rather than swallowed;
   - `drainAndWait` against a real subprocess (`sh -c`) that emits an over-ceiling line
     followed by enough output to fill the pipe, asserting it returns within a
     per-test timeout rather than deadlocking. Argument- and parser-level tests cannot
     catch this failure; only a live process can.

### Phase 3: Add opt-in all-branches history

**ID:** `3`
**Goal:** with the setting enabled, the Commit Log shows commits from every local
branch, remote-tracking branch and tag, each labelled by its refs
**Tests:** pending

**Acceptance criteria:**

- [ ] `CommitLogShowAllBranches` exists in `GittiConfigSettings` as
      `commit_log_show_all_branches`, defaulting to `false`, and survives a restart when set
      to `true` — and, after Phase 1, when set back to `false`.
- [ ] When enabled, `buildCommitLogArgs` inserts
      `--branches --remotes --tags --ignore-missing HEAD` before the trailing `--`, not
      after it.
- [ ] `refs/stash`, `refs/notes/*` and other non-branch namespaces never appear as
      commit rows in either mode.
- [ ] An unborn `HEAD` (after `git switch --orphan`) still yields the full branch
      history rather than an empty log.
- [ ] An empty repository yields an empty log without an error row.
- [ ] `gitti --commit-log-show-all-branches true|false` persists the setting, prints a
      localized confirmation and exits, rejecting any other value.
- [ ] The new flag and its three status strings are present in `en`, `ja`, `zh-hans`
      and `zh-hant`.
- [ ] Default behaviour with the setting off is unchanged from Phase 2.

**Steps:**

1. Add the field and default in `settings/settings.go` plus an
   `UpdateCommitLogShowAllBranches` setter following the `UpdateFfMerge` shape.
2. Add `config.SetCommitLogShowAllBranches` following `config.SetFfMerge`.
3. Add the flag and its `switch` case in `main.go`.
4. Add `FlagCommitLogShowAllBranches`, `CommitLogShowAllBranchesEnabled`,
   `CommitLogShowAllBranchesDisabled` and `CommitLogShowAllBranchesSetError` to
   `i18n/types.go` and all four locale files.
5. Thread the setting into `InitGitCommitLog` at `api/utils.go:97` and into
   `buildCommitLogArgs`. That call site is inside `api.InitGitOperations`, which is also
   re-run on a worktree switch (`tui/services/worktree.go:147`), so reading
   `settings.GITTICONFIGSETTINGS.CommitLogShowAllBranches` there covers both startup and
   worktree changes with no second read site.
6. Extend `api/git/commitlog_test.go` to assert the argument set for both modes.

### Phase 4: Show target refs on commit-targeting actions

**ID:** `4`
**Goal:** every action that acts on a selected commit shows that commit's refs, and no
popup asserts a branch provenance it cannot know
**Tests:** pending

The two popup families behave differently on purpose, and the criteria state which is
which rather than claiming both are unchanged.

**Acceptance criteria — unconditional (both modes):**

- [ ] The reset, revert and create-tag confirmation popups show the target commit's
      refs beside its hash whenever it has refs. This is a visible change against
      today's output for such commits, in both modes, and is intended.
- [ ] When the target commit has no refs, those popups render exactly as today.

**Acceptance criteria — conditional on all-branches mode:**

- [ ] With all-branches mode **off**, the cherry-pick popup renders byte-identically to
      today: every item is labelled with the current branch name.
- [ ] With all-branches mode **on**, each cherry-pick item is labelled with that
      commit's own refs.
- [ ] In that mode the cherry-pick delegate omits the "Cherry picked from branch ~>"
      line entirely when the commit has no refs, rather than rendering a dangling label
      or a false branch name.

**Acceptance criteria — gate correction:**

- [ ] The reset-latest gate is the predicate "`HEAD~1` resolves to a commit", answered
      by git rather than inferred from the panel list, so it returns the same result in
      both modes and cannot open a popup whose `HEAD~1` reset would fail.
- [ ] The gate is `false` on an unborn `HEAD` and on a repository with exactly one
      commit, and `true` from the second commit onward.

**Steps:**

1. Thread `Refs` from `GitCommitLogItem` through
   `InitGitResetToSelectedCommitTypeOptionPopUpModel` and render it in
   `tui/popup/commit/render.go:249-265`; do the same for the revert confirmation
   (`ctrl_r.go`) and the create-tag popup (`t.go`).
2. Add `Refs` to `GitCherryPickItem` and `GitEditCherryPickItem`; source the label
   conditionally in `InitGitCherryPickPopUpModel` and suppress the empty line in the
   delegates.
3. Replace the panel-derived gate in
   `tui/interaction/handler/nontyping/shift_r.go:25` with a call to a new
   `api/git` helper running `rev-parse --verify --quiet HEAD~1` and reporting success as
   a bool. A subprocess on a keypress is consistent with the rest of the handlers, and
   `api/git/utils.go:161` already runs `rev-parse` in the same style. Deriving it from
   the commit-log data instead would reintroduce the mode dependence this phase removes.

### Phase 5: Full test sweep

**ID:** `5`
**Goal:** every test declared by this spec is green together
**Tests:** all

**Acceptance criteria:**

- [ ] The union of test paths declared by completed phases passes through the scoped
      resolver.
- [ ] Failures surfaced by the sweep are remediated in this phase.

### Phase 6: Outcome

**ID:** `6`

Reconcile delivered behavior, deviations, decisions, deferred work, and the full-sweep
result into `Outcome`.

### Phase 7: Documentation

**ID:** `7`

Invoke `/ckit:docs` for the shipped behavior and verification instructions. Covers the
README Features entry, both new flags, the documented limitation that the commit count
applies across the union in all-branches mode, a per-setting statement of exactly which
surfaces it affects and which it does not, and — prominently — the upgrade note for
the Phase 1 repair. That note must tell users their earlier setting was *overwritten on
disk* and needs re-applying (`re-run --auto-update false` /
`--allow-commit-graph-write false` after upgrading), not that it now takes effect
automatically. See Design, "Configuration", for why the intuitive phrasing is wrong.

## 4. Verification

Reference view for every scenario:

```bash
git log --all --topo-order --decorate=short --graph --oneline -n 100
```

Gitti need not draw identical ASCII lanes, but topology, branch tips, `HEAD` location,
tags and remote refs must agree.

- [ ] **A. Current branch only.** `A--B--C` with `HEAD` at `C`: `C` shows
      `[HEAD -> <branch>]`, `B` and `A` show nothing.
- [ ] **B. Two diverged branches.** With all-branches on, both tips appear, labelled
      `[master]` and `[feature]`, and the graph shows the fork.
- [ ] **C. Remote branch.** `[origin/master]` renders; `origin/HEAD` does not.
- [ ] **D. Local and remote on one commit.** `[master, origin/master]`.
- [ ] **E. Tag.** `[tag: v0.9.0]`.
- [ ] **F. HEAD.** `[HEAD -> <branch>]`.
- [ ] **G. Detached HEAD.** Renders cleanly, assuming no branch name.
- [ ] **H. Filtering.** Filtering on a branch-name fragment keeps that commit visible
      even though its subject does not contain the fragment.
- [ ] **I. Commit limit.** The existing maximum still applies with all-branches on.
- [ ] **J. Hostile decoration config.** With `log.excludeDecoration='refs/tags/*'` and
      again with `log.initialDecorationSet=all`, rows show exactly the pinned set.
- [ ] **K. Stash and notes present.** Neither contributes commit rows in either mode.
- [ ] **L. Unborn HEAD and empty repository.** Neither produces a fatal or an empty
      log where branches exist.
- [ ] **M. Truncated multi-tip graph.** With the commit limit set low enough to cut a
      multi-tip history, lanes left open at the truncation boundary do not corrupt the
      rows above it.
- [ ] **N. Narrow panel.** At the default left-panel ratio on an 80-column terminal, a
      long ref does not evict the subject entirely, and the subject keeps at least 15
      columns on every row that shows refs.
- [ ] **O. Wide lane block.** In a repository whose all-branches graph reaches enough
      concurrent lanes to leave under 30 columns after the lane, the ref block is
      omitted rather than shrinking the subject further.
- [ ] **P. Reset-latest gate.** With all-branches on, in a repository whose `HEAD` has
      exactly one commit but whose other branches contribute rows, the reset-latest
      popup does not open.
- [ ] **Q. Detached HEAD in a second worktree.** Its commit is not walked; this is a
      documented non-goal, not a defect.
- [ ] **R. Refs off.** With `commit_log_show_refs: false`, Commit Log rows are
      indistinguishable from the pre-change build, and filtering on a branch-name
      fragment no longer matches commits whose subject lacks it.
- [ ] **S. Boolean persistence.** `commit_log_show_refs: false`, `auto_update: false`
      and `allow_commit_graph_write: false` each survive a restart; an absent key still
      picks up its default and is written back.
- [ ] **T. Non-boolean guards intact.** `max_commit_log_count: 0` still falls back to
      the default rather than being honoured.

## Outcome

<!-- Build fills this after implementation, including the full-sweep result. Keep it brief. -->
