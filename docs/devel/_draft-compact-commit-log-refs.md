---
status: draft
issue: null
pr: null
completed: []
---

# Compact Commit Log Ref Decorations — Design Document

Issue [#1](https://github.com/amazeika/gitti/issues/1) attached Git's ref decorations to
Commit Log rows, rendering `%D` verbatim. In use that string proved too wide to appear at
all on ordinary terminals: it needed roughly 160 columns at the default panel ratio, so
the feature was invisible to most users. This change rewrites the decoration list into a
compact form and relaxes the width floor that the verbatim string had required. It
supersedes one acceptance criterion of the shipped specification.

## Contents

1. [Motivation](#1-motivation)
2. [Design](#2-design)
3. [Acceptance criteria](#3-acceptance-criteria)
4. [Test instructions](#4-test-instructions)

## 1. Motivation

Git names a pushed branch once per ref that points at the commit. On this repository the
tip commit decorates as:

```
HEAD -> refs/heads/feature/1-commit-log-refs-and-all-branches, refs/remotes/origin/feature/1-commit-log-refs-and-all-branches
```

125 columns to convey one 41-character branch name. The dominant cost is not the
`HEAD -> ` and `origin/` labels but the **repetition**: the same branch is printed once
as a local ref and once per remote. Shortening the labels alone recovers 13 columns of
125; collapsing the duplicates recovers 79.

The shipped width policy compounded it. A block was drawn only when 30 columns remained
after the hash, monogram and lane, which at the default `left_panel_width_ratio` of 0.30
requires a terminal about 160 columns wide. Below that the row silently drew nothing,
which reads as the feature being broken rather than as the row being narrow.

## 2. Design

### 2.1 Namespace-qualified decorations

`%D` under the default short form is ambiguous: `origin/x` is either a remote-tracking
branch or a local branch literally named `origin/x`, and nothing in the string
distinguishes them. Collapsing duplicates safely requires knowing which is which, so the
log now passes `--decorate=full` and reads `refs/heads/…`, `refs/remotes/…` and
`refs/tags/…`. The `tag:` marker survives `--decorate=full` and is stripped separately.

### 2.2 Compaction

`api/git/decorations.go` parses the list into classified entries and renders them as:

| Raw | Compact | Meaning |
| --- | --- | --- |
| `HEAD -> refs/heads/main, refs/remotes/origin/main` | `*main^` | checked out, and a remote is here too |
| `HEAD -> refs/heads/wip` | `*wip` | checked out, not pushed |
| `refs/heads/main, refs/remotes/origin/main` | `main^` | not checked out, pushed |
| `refs/heads/wip` | `wip` | not checked out, not pushed |
| `refs/remotes/origin/release` | `origin/release` | remote-only: no local branch to collapse into |
| `tag: refs/tags/v0.9.0` | `v0.9.0` | tag, told apart by colour |
| `HEAD` | `*HEAD` | detached; the row already prints the hash |

`*` replaces `HEAD -> `. It appears at most once in a whole log, and earns its keep in
all-branches mode where many branch tips are visible at once. `^` replaces the duplicate
remote entry and means "at least one remote points here too", so a branch on several
remotes costs no more room than one.

The marker is ASCII deliberately. `°` and `·` are East Asian Ambiguous width: a terminal
configured for one of the three CJK locales gitti ships draws them two columns wide while
`ansi.StringWidth` measures one, misaligning the row and overrunning a width budget taken
against that measurement.

Entries render in a fixed order — checked-out branch, other local branches, remote-only
branches, tags, anything unrecognised — rather than git's, which varies with the order the
refs were written. A ref outside the three expected namespaces is shown as written rather
than dropped, so an unexpected decoration is visible instead of silently missing.

### 2.3 Filtering keeps what the row drops

The row shows `main^`, but someone filtering by `origin/main` must still find the commit.
`GitCommitLogItem` therefore carries the compact string for display and a separate
`RefsFilter` holding every name in the short form git would have printed, remote
duplicates included. The namespaces are omitted from it: leaving `refs/heads/` in would
make a filter of `heads` match every decorated commit.

### 2.4 Width policy

The floor drops from 30 columns to 18, which is what the unchanged half-width cap needs to
still clear the shortest useful block: 18/2 less the 3-column overhead leaves 6 columns,
enough for a short branch name and its markers. The cap stays at half, so a long branch
name still gets the room a third would have denied it.

This supersedes the shipped criterion at
[`1-commit-log-refs-and-all-branches.md`](1-commit-log-refs-and-all-branches.md) lines
554–556, which required a 30-column floor and guaranteed the subject at least 15 columns.
The guarantee is now "never more than half the row", which at the floor leaves the subject
9 columns.

Measured through the delegate, refs appear at:

| Terminal | Was | Now |
| --- | --- | --- |
| 80 columns | ratio 0.65 only | ratio 0.50 |
| 100 columns | ratio 0.50 | ratio 0.40 |
| 120 columns | ratio 0.40 | ratio 0.30 (the default) |

### 2.5 Cherry-pick source label

`cherryPickSourceLabel` printed the raw decoration list under a "from branch" label, so a
tagged commit was labelled with its tag. It now takes a single branch name derived from
the decorations — checked-out branch, else any local branch, else a remote-only one — and
yields nothing for a commit decorated solely by tags, which is truthful where naming the
tag was not. This closes a follow-up recorded in the shipped specification's Outcome.

## 3. Acceptance criteria

- [ ] A local branch and the remote-tracking branches that match it render as one entry
      carrying a trailing remote marker, not once per ref.
- [ ] The checked-out branch carries a leading marker in place of `HEAD -> `, and at most
      one row in the log carries it.
- [ ] A branch existing only on a remote keeps its remote prefix, because there the remote
      name is the information rather than a duplicate.
- [ ] A local branch named like a remote-tracking branch is not mistaken for one.
- [ ] Tags render without the `tag:` marker or the `refs/tags/` namespace.
- [ ] Filtering matches a remote name the row no longer draws.
- [ ] The ref block is omitted when fewer than 18 columns remain after the hash, monogram
      and lane, and is otherwise truncated to at most half of that remaining width.
- [ ] A commit decorated only by tags reports no source branch in the cherry-pick list.

## 4. Test instructions

Automated:

```bash
go test ./api/git/ ./tui/component/commitlog/
```

`api/git/decorations_test.go` covers parsing, collapsing, ordering and the filter and
branch-label derivations. `tui/component/commitlog/types_test.go` covers the width policy
and that filtering is not narrowed by what the row draws.

Manual:

1. `gitti --commit-log-show-refs true` and `gitti --commit-log-show-all-branches true`,
   then restart gitti in a repository with a pushed branch, an unpushed branch and a tag.
2. Confirm the checked-out branch row reads `[*<branch>^]` when pushed and `[*<branch>]`
   when not, and that exactly one row carries `*`.
3. Confirm a tag renders as its bare name and a remote-only branch keeps `origin/`.
4. Press `-` until the ref blocks disappear, then `+` until they return, and confirm the
   subject is never pushed off the row.
5. Filter the Commit Log by a remote name such as `origin/main` and confirm the commit
   still matches even though the row shows only `main^`.
