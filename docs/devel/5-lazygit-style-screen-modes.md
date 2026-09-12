---
status: in-progress
issue: 5
pr: null
completed: [1]
---

# Lazygit-Style Screen Modes — Design Document

**Issue:** [#5 — Layout: lazygit-style screen modes (two-column, single-column, focused)](https://github.com/amazeika/gitti/issues/5)

**Baseline:** `main` at `84e1ad4`

Gitti currently has one fixed frame: five primary panels stacked on the left, detail and
application-log panels stacked on the right, and a keybinding bar at the bottom. This
change adds two alternative runtime layouts without changing Git operations, loaded
data, panel contents, or the existing default.

## 1. Goals and scope

### Goals

1. Add three screen modes:
   - **Two-column**: today's layout, unchanged and the startup default.
   - **Single-column**: the existing five-panel primary stack at full width; the detail
     and application-log column is absent.
   - **Focused**: exactly the selected panel occupies the full main content area.
2. Bind `=` to cycle forward and `_` to cycle backward.
3. Keep the bottom keybinding bar visible in every mode and advertise both mode keys
   there whenever no popup is open.
4. Reuse the existing dynamic stack-height policy in both two-column and single-column
   modes.
5. Make keyboard and mouse focus agree with what is visible; hidden detail or log
   panels must never retain actionable focus in single-column mode.
6. Preserve mode across an in-process worktree switch, but reset to two-column on a new
   process, matching lazygit's runtime-only behavior.

### Non-goals

- Persisting screen mode in `settings.GittiConfigSettings` or adding a CLI flag.
- Changing `left_panel_width_ratio`, its persisted setting, or its bounds.
- Replacing the existing focus model or changing the order of
  `constant.ComponentPanelNavigationList`.
- Adding, removing, or changing Git/data APIs, background refreshes, filters, or panel
  actions.
- Making primary lists mouse-wheel navigable. Their existing keyboard, paging, and
  click-selection behavior remains authoritative.
- Redesigning popup dimensions or popup-specific keybinding bars.
- Lowering `constant.MinWidth` or `constant.MinHeight` for single/focused mode.

## 2. User-visible behavior

### Mode and panel terminology

The primary stack is the existing navigation list:

| Index | Component | Visible in single-column |
| ---: | --- | :---: |
| 0 | `GitStatusComponentPanel` (`C0`) | yes |
| 1 | `LocalBranchOrTagOrRemoteOrWorktreeComponentPanel` (`C1`) | yes |
| 2 | `ModifiedFilesComponentPanel` (`C2`) | yes |
| 3 | `CommitLogOrRefLogComponentPanel` (`C3`) | yes |
| 4 | `StashComponentPanel` (`C4`) | yes |

The extended panels are `DetailComponentPanel` (`EC-DT`),
`DetailComponentPanelTwo` (`EC-DT2`), and `LogComponentPanel` (`L0`).

The “full” area below means the terminal area above the one-row keybinding bar. Panel
borders remain part of the layout; focused mode does not remove the selected border or
other panel styling.

### Rendering contract

| Mode | Main content |
| --- | --- |
| Two-column | The existing left primary stack and right detail/log stack. Width ratio, selected-stack-height behavior, detail subpanel split, and log height remain unchanged. |
| Single-column | The same five primary panels, in the same order and with the same dynamic heights as the current left stack, but using the full terminal width. No detail or log panel is rendered. |
| Focused | One panel only: `CurrentSelectedComponent`, full width and full main-content height. |

Focused mode treats the selected component literally:

- `C0` renders only Git status, with its existing content at the top of a full-height
  bordered panel.
- `C1`–`C4` render the selected list panel with a full-width/full-height list model.
- `EC-DT` renders only the primary detail viewport.
- `EC-DT2` renders only the secondary detail viewport. The other detail viewport is not
  rendered even when `ShowDetailPanelTwo` is true.
- `L0` renders `CurrentLogComponentViewport`, not the full-history detail viewport.

For line editing, focused `EC-DT` or `EC-DT2` retains the line-editing title and the
cursor column, but only the selected diff subpanel is drawn. `ShowDetailPanelTwo`, both
viewport contents, cursor positions, and offsets remain intact, so `[`/`]` can switch
between the two full-screen subpanels and returning to two-column restores the normal
adaptive split.

The terminal-size warning in `GittiMainPageView` takes precedence over all three modes.
The existing 80x24 minimum remains unchanged.

### Cycling contract

For a primary component (`C0`–`C4`), the cycle is:

```text
= : TWO_COLUMN -> SINGLE_COLUMN -> FOCUSED -> TWO_COLUMN
_ : TWO_COLUMN -> FOCUSED -> SINGLE_COLUMN -> TWO_COLUMN
```

Single-column cannot represent `EC-DT`, `EC-DT2`, or `L0`. For one of those selected
components, cycling skips the incompatible mode without changing focus:

```text
TWO_COLUMN <-> FOCUSED
```

This is deliberate. Silently moving from a detail viewport back to its parent would
lose the user's detail/subpanel and line-editing context; retaining extended focus in a
layout that does not render it would create hidden focus. Skipping single-column avoids
both failures. A defensive reflow guard must repair any otherwise-impossible
`SINGLE_COLUMN + extended component` state by promoting the mode to `FOCUSED`, without
changing the selected component.

### Enter, Escape, slash, and primary navigation

- In single-column mode, a successful Enter drill-down from Modified Files, Commit
  Log/Reflog, or Stash sets the normal detail parent and changes the mode to Focused in
  the same update. An Enter that opens an existing branch/remote/worktree popup or does
  nothing does not change mode.
- `/` in single-column selects `L0` and changes the mode to Focused. In two-column and
  focused modes it retains today's mode while selecting `L0`.
- Escape from a focused detail subpanel returns to `DetailPanelParentComponent` while
  retaining Focused mode; the parent primary panel is immediately reflowed full-screen.
  Escape's existing first-step exit from line-editing mode is unchanged.
- Tab, Shift+Tab, number keys `1`–`4`, and primary-panel mouse clicks retain the current
  screen mode. In Focused mode they replace the sole visible panel. Existing boundary
  behavior (no wrap at either end) remains unchanged.
- `[` and `]` retain their current meaning. In Focused mode, changing detail subpanel
  immediately replaces the full-screen viewport.

### Ratio keys

`+` and `-` modify `WindowLeftPanelRatio` and trigger reflow only in Two-column mode.
They are no-ops in Single-column and Focused modes, and they do not alter the stored
ratio. Returning to Two-column therefore restores the user's prior split.

`_` is distinct from `-`: it is always the backward mode key when ordinary main-page
key handling is active.

### Popup and text-input precedence

An open popup remains an overlay over the current base mode. It does not become the
focused panel and does not alter `ScreenMode`.

- `=` and `_` do not change modes while any popup is open, including non-typing popups.
- Popup-specific key help remains the only help shown in the bottom bar while a popup
  is open.
- While panel-filter input is active, `=` and `_` continue through the filter-input path
  as query text; they do not change modes.
- Existing typing-popup dispatch and global Ctrl bindings are unchanged.

## 3. State model

Add a typed screen-mode enum in `tui/constant/constant.go`:

```go
type ScreenMode uint8

const (
    ScreenModeTwoColumn ScreenMode = iota
    ScreenModeSingleColumn
    ScreenModeFocused
)
```

Add `ScreenMode constant.ScreenMode` to `types.GittiModel` in
`tui/types/types.go`. `InitGittiModel` initializes it to `ScreenModeTwoColumn`.
There is no config field.

`ReinitGittiModel` must not overwrite `ScreenMode`, just as it intentionally preserves
terminal dimensions. Its function comment must name all three preserved runtime
properties: Width, Height, and ScreenMode. Because reinitialization resets focus to
Modified Files, preserving Focused yields a full-screen Modified Files panel and
preserving Single-column yields the full primary stack after the switch.

The model invariant is:

```text
ScreenModeSingleColumn => CurrentSelectedComponent is one of C0..C4
```

Mode-cycling and single-column drill-down enforce the invariant at interaction time;
mode-aware reflow enforces it defensively before calculating dimensions.

## 4. Layout and rendering design

### One canonical reflow

Make `layout.TuiWindowSizing` the canonical mode-aware reflow operation. It must be safe
to call after either terminal dimensions or focus/mode changes. Avoid a second set of
independent sizing formulas.

The operation has four responsibilities:

1. Calculate shared main-content dimensions and mode-specific visible widths.
2. Reuse `LeftPanelDynamicResize` for the five-panel stack in Two-column and
   Single-column modes. Single-column changes the stack width, not its height policy.
3. Size the selected list, detail viewport(s), cursor viewport(s), or log viewport for
   Focused mode.
4. Rebuild list titles and reconcile viewport offsets after widths/heights are set.

Two-column calculations are regression-sensitive and remain behaviorally identical to
baseline. In Single-column, the stack width and list title limits derive from the full
window width. In Focused, the selected renderer receives the full window width and
`WindowCoreContentHeight`; list and viewport inner dimensions continue to account for
existing borders, cursor columns, and the line-editing title.

Do not call the current percentage-based horizontal-offset adjustment repeatedly as a
side effect of ordinary focus changes. Capture each viewport's X/Y offset before
resizing and restore it when that position remains valid. If a larger viewport makes an
offset invalid, clamp it to the viewport's new maximum and synchronize
`DetailPanelViewportOffset` / `DetailPanelTwoViewportOffset` with that actual value. The
clamp is intentionally lossy: returning to a narrower mode does not resurrect an offset
that ceased to exist in the wider viewport. A mode change must never reset an offset
that remains valid. `EnterOrReinitLineEditingState` still runs after detail/cursor
viewport dimensions are final so its visible cursor index is recalculated against the
new height.

Every mutation that can change the sole visible panel must use the canonical reflow,
not only `LeftPanelDynamicResize`. The required call sites are:

- `tea.WindowSizeMsg`;
- the new `=` and `_` handlers;
- Tab, Shift+Tab, digits `1`–`4`, Enter drill-down, Escape detail return, `/`,
  `[`/`]`, and the `<`/`>` shared-panel variant changes;
- mode-aware left-click focus changes;
- both detail-layout event paths in `helper.GittiTuiUpdateEventHelper`: after
  `UpdateDetailComponentViewportContentAndState`, and after entering or exiting
  line-editing mode through `DETAIL_COMPONENT_PANEL_LAYOUT_UPDATED_EVENT`; and
- the successful worktree-switch path after `ReinitGittiModel`.

Those helper paths must finish with `TuiWindowSizing`, not the narrower
`UpdateDetailComponentViewportLayout`. This covers `EC-DT2` normalization and ensures
that adding/removing the line-editing title and cursor column reflows a focused detail
panel immediately.

Data refreshes should not otherwise trigger a mode transition or extra detail fetch.
Changing mode is a local layout operation and emits no Git/daemon command.

### Renderer decomposition

Refactor `GittiMainPageView` so terminal validation, bottom-bar composition, and popup
compositing remain shared, while a small mode switch assembles main content:

- Two-column calls the current left-stack and right-stack composition.
- Single-column calls the same primary-stack renderer at full width.
- Focused dispatches one component renderer from `CurrentSelectedComponent`.

Generalize the Git-status renderer to accept width and height so C0 can fill focused
mode without changing its text. Add a focused detail rendering path (or an explicit
render option) rather than mutating `ShowDetailPanelTwo`: the normal detail renderer
must continue to compose both subpanels in Two-column mode, while focused rendering
must compose only the selected subpanel.

The keybinding bar is joined after the mode-specific main content in all cases. Popup
composition remains the final layer over the completed base view, so opening and
closing a popup reveals exactly the same mode beneath it.

### Geometry and fit

The rendered base view must occupy the terminal without wrapping or adding phantom
rows in all modes at and above 80x24. Centralize border-aware panel footprints used by
mouse hit-testing so click regions match the pre-click rendered geometry. The bottom
keybinding row is never a panel hit target.

## 5. Input and mouse design

### Keyboard handlers

Following repository convention, add one handler file per key under
`tui/interaction/handler/nontyping/` (for example, `equal.go` and `underscore.go`) and a
small shared cycle helper. Register `=` and `_` in `dispatch.go`.

The helper takes a direction, chooses the next compatible mode according to the cycle
contract, stores it, and calls `layout.TuiWindowSizing`. Both handlers return no
command. Each handler explicitly refuses to act when `ShowPopUp` is true; filter and
popup typing already intercept earlier in `GittiKeyInteraction`.

Change the existing `+`/`-` dispatch branches to test
`ScreenMode == ScreenModeTwoColumn` as well as `!ShowPopUp` before changing the ratio.

### Click hit-testing

Resolve a click against the geometry that was visible before the click, then mutate
focus and reflow:

- Two-column retains the current left stack and right detail/log regions. A detail area
  with one viewport selects `EC-DT`. When `ShowDetailPanelTwo` is true and line editing
  is inactive, its horizontal or vertical split produces two non-overlapping hit
  rectangles matching the rendered panel footprints; clicking a rectangle selects
  `EC-DT` or `EC-DT2` respectively. Each rendered border belongs to the panel whose
  footprint contains it, including the adjacent borders at the internal split.
- Single-column recognizes only the full-width five-panel stack.
- Focused recognizes only the selected panel. For a focused list, row-click selection
  uses the list's full-height paginator; clicking cannot focus a hidden component.
- Clicks remain disabled while line editing, so `[`/`]` are still the only way to change
  diff subpanel in that state.
- Clicks on the keybinding bar, outside the terminal content, or in an absent panel are
  no-ops.

`selectListItemFromClick` remains the row-selection mechanism. Its item row must still
exclude the border and title row, and selection must still be resolved before a focus
change resizes the stack.

### Wheel routing

Popup wheel handling is unchanged. On the main page, route wheel input only to a
visible viewport:

| Mode / pointer region | Wheel target |
| --- | --- |
| Two-column detail region | selected detail viewport (`EC-DT2` when selected, otherwise `EC-DT`) |
| Two-column log region | `CurrentLogComponentViewport` |
| Two-column primary stack | no-op |
| Single-column | no-op |
| Focused `EC-DT` / `EC-DT2` | selected detail viewport |
| Focused `L0` | `CurrentLogComponentViewport` |
| Focused primary panel | no-op |
| Bottom bar | no-op |

Horizontal and vertical wheel directions use the same visibility/region resolver. Line
editing preserves baseline direction behavior after target resolution: vertical wheel
input is a no-op, while horizontal wheel input may scroll the resolved visible viewport.
This removes the current possibility of scrolling a hidden detail viewport in
Single-column or Focused log mode without expanding scope into list-wheel navigation.

## 6. Keybinding bar and localization

Add localized fields to `i18n.LanguageMapping`, mirroring the existing page-navigation
hint:

```go
ScreenModeNavigationKey         string // "=/_"
ScreenModeNavigationDescription string // localized "screen mode"
```

Populate English, Japanese, Simplified Chinese, and Traditional Chinese. On a normal
main page, prepend:

```text
[=/_] <localized screen mode description>
```

before page-navigation and panel-specific hints, so width truncation preserves mode
discoverability first. Existing context-sensitive panel keys and the right-aligned app
version remain unchanged. Do not prepend the mode hint to popup key bars, because mode
changes are blocked there.

`i18n.TestEveryLocaleTranslatesEveryString` provides parity coverage for the new string
fields.

## 7. Implementation

### Phase 1: State and canonical reflow

**ID:** `1`
**Goal:** Screen mode is explicit runtime state, and one canonical reflow safely sizes every
mode after terminal, focus, detail-layout, and worktree changes.
**Tests:** `tui/initialize/initialize_test.go`, `tui/layout/utils_test.go`
**Files:** `tui/constant/constant.go`, `tui/types/types.go`,
`tui/initialize/initialize.go`, `tui/layout/utils.go`,
`tui/helper/tui-update-helper.go`, and focus-changing handlers.

**Acceptance criteria:**

- [x] Add the typed enum and model field.
- [x] Default startup to Two-column and preserve mode during worktree reinit.
- [x] Make `TuiWindowSizing` mode-aware and offset-preserving.
- [x] Replace partial focus-only resize calls with canonical reflow at the enumerated call
      sites.
- [x] Enforce the single-column focus invariant.

### Phase 2: Mode-specific rendering

**ID:** `2`
**Goal:** The main page assembles the documented Two-column, Single-column, and Focused views
without changing shared footer, warning, or popup behavior.
**Tests:** pending
**Files:** `tui/layout/view.go`, `tui/layout/render.go`.

**Acceptance criteria:**

- [ ] Extract reusable primary-stack and right-stack composition.
- [ ] Add Single-column and Focused assembly.
- [ ] Generalize Git-status dimensions.
- [ ] Render one selected detail subpanel in Focused mode while preserving dual-detail
      state.
- [ ] Keep footer, minimum-size warning, and popup composition shared.

### Phase 3: Keyboard and mouse interaction

**ID:** `3`
**Goal:** Keyboard and mouse interactions obey mode compatibility and act only on visible panels.
**Tests:** pending
**Files:** `tui/interaction/handler/nontyping/dispatch.go`, new per-key handler files,
`enter.go`, `esc.go`, `slash.go`, navigation handlers, `tui/interaction/click.go`, and
`tui/interaction/mouse.go`.

**Acceptance criteria:**

- [ ] Implement directional compatible-mode cycling.
- [ ] Promote successful Single-column drill-down and slash navigation to Focused.
- [ ] Limit ratio keys to Two-column.
- [ ] Make click and wheel routing mode/visibility aware.

### Phase 4: Discoverability and regression coverage

**ID:** `4`
**Goal:** Every locale advertises mode navigation, and automated regressions cover the complete
screen-mode contract.
**Tests:** pending
**Files:** `i18n/types.go`, all four locale files, and new tests beside the affected
packages.

**Acceptance criteria:**

- [ ] Add and render the normal-page screen-mode hint.
- [ ] Add table-driven state, layout, rendering, navigation, and mouse tests.
- [ ] Run repository validation and the manual matrix below.

### Phase 5: Full test sweep

**ID:** `5`
**Goal:** every test declared by this spec is green together
**Tests:** all

**Acceptance criteria:**

- [ ] The union of test paths declared by completed phases passes through the scoped resolver.
- [ ] Failures surfaced by the sweep are remediated in this phase.

### Phase 6: Outcome

**ID:** `6`

Reconcile delivered behavior, deviations, decisions, deferred work, and the full-sweep result into
`Outcome`.

### Phase 7: Documentation

**ID:** `7`

Invoke `$ckit:docs` for the shipped behavior and verification instructions.

## 8. Test plan

### Automated tests

Add table-driven tests that cover at least:

1. **Cycle matrix**
   - Forward and reverse cycles for each primary mode.
   - Detail, second-detail, and log skip Single-column in both directions.
   - A corrupt Single-column/extended state is repaired to Focused without changing
     selected component.
   - Popup state blocks both keys; panel-filter dispatch treats the characters as input.
2. **Ratio behavior**
   - `+`/`-` change and clamp ratio only in Two-column.
   - Single-column and Focused preserve the ratio for a later Two-column render.
3. **Sizing and render visibility**
   - Two-column dimensions match baseline formulas.
   - Single-column uses full width and the same stack-height distribution.
   - Every focused component receives full main-area dimensions.
   - Focused `EC-DT2` output excludes `EC-DT`, and vice versa.
   - Dual-detail line editing retains both contents/indices across Two-column ↔ Focused
     transitions, and entering/exiting line editing while already Focused immediately
     recomputes title, cursor, and content dimensions.
   - X/Y offsets are retained when valid; widening clamps invalid offsets permanently,
     and returning to a narrower mode keeps the clamped value.
   - Rendered base views fit representative 80x24 and wide terminal dimensions.
4. **Navigation invariants**
   - Successful Enter drill-down and `/` promote Single-column to Focused.
   - No-op or popup-opening Enter paths retain Single-column.
   - Escape, Tab, Shift+Tab, digits, brackets, and angle-bracket variant changes reflow
     the newly visible focused panel without changing mode.
   - Reinit preserves mode but resets focus to Modified Files.
5. **Mouse**
   - Click boundaries for all three modes, including both dual-detail split
     orientations, adjacent split borders, title rows, full-height paginator row
     selection, line-editing click suppression, and bottom-bar no-op behavior.
   - Wheel events affect only the visible target in the routing table and never hidden
     detail state; line editing blocks vertical wheels but retains horizontal wheels.
6. **Help and popups**
   - Normal key bars contain `[=/_]` before other hints.
   - Popup key bars omit it.
   - A popup overlays and closes back onto the same base mode.
   - Every locale supplies both new strings.

Suggested test locations are `tui/layout/utils_test.go`, `tui/layout/view_test.go`,
`tui/interaction/handler/nontyping/screen_mode_test.go`,
`tui/interaction/click_test.go`, and `tui/interaction/mouse_test.go`. Tests may use
package-local helpers; production APIs need not be exported solely for testing.

### Manual matrix

At minimum, exercise 80x24 and a wide terminal:

- cycle forward and backward from each primary panel;
- enter/escape a modified-file diff, commit detail, stash detail, and application log;
- toggle `[`/`]` with a two-part staged/unstaged diff, both normally and during line
  editing;
- resize while in every mode and while scrolled horizontally/vertically;
- open and close a typing popup and a non-typing popup in every mode;
- click panel headers/items/borders and use all wheel directions;
- adjust ratio, leave Two-column, return, and verify the ratio is preserved; and
- switch worktrees in each mode and verify the mode survives while repository-local
  selection/filter/detail state resets.

Repository gates:

```sh
gofmt -w <changed-go-files>
go test ./...
go vet ./...
```

## 9. Acceptance criteria

- Gitti starts in the unchanged Two-column layout.
- `=` and `_` traverse the documented compatible modes and are visible in the normal
  bottom bar in every locale.
- Single-column shows only the full-width five-panel primary stack and never has hidden
  detail/log focus.
- Focused shows exactly one selected primary, detail subpanel, or log panel over the
  full main content area.
- Existing panel operations, filters, selection indices, line-editing state, popup
  workflows, and Git refresh behavior survive mode changes. Viewport offsets survive
  while valid and otherwise clamp to the resized viewport's bounds.
- `+`/`-` affect the split only in Two-column and the split returns unchanged after
  visiting other modes.
- Mouse clicks and wheels cannot focus or mutate a panel that is not visible.
- Popups remain overlays, block mode changes, and close back to the same mode.
- Mode survives worktree switching but is not persisted across process restarts.
- All automated tests, `go vet ./...`, and gofmt cleanliness pass.
