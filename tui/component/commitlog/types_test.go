package commitlog

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"github.com/charmbracelet/x/ansi"

	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/settings"
	"github.com/gohyuhan/gitti/tui/constant"
)

// ------------------------------------
//
//	Point the package-level settings at a config with refs on or off for the
//	duration of one test
//
// ------------------------------------
func withShowRefs(t *testing.T, showRefs bool) {
	t.Helper()

	original := settings.GITTICONFIGSETTINGS
	t.Cleanup(func() { settings.GITTICONFIGSETTINGS = original })

	cfg := settings.GittiDefaultConfigSettings
	cfg.CommitLogShowRefs = showRefs
	settings.GITTICONFIGSETTINGS = &cfg
}

func TestFilterValueMatchesARef(t *testing.T) {
	withShowRefs(t, true)

	item := GitCommitLogItem{Hash: "abc1234", Refs: "feature-x^", RefsFilter: "feature-x origin/feature-x", Message: "tidy up", Author: "Ada"}

	if !strings.Contains(item.FilterValue(), "origin/feature-x") {
		t.Errorf("FilterValue = %q, want the branch name so a filter can isolate its commits", item.FilterValue())
	}
}

func TestFilterValueMatchesARemoteNameTheRowNoLongerDraws(t *testing.T) {
	withShowRefs(t, true)

	// The row collapses a pushed branch to "feature-x^", dropping the separate
	// origin entry. Someone who types the remote form must still find the commit.
	const decorated = "HEAD -> refs/heads/feature-x, refs/remotes/origin/feature-x"
	item := GitCommitLogItem{
		Hash:       "abc1234",
		Refs:       git.CompactDecorations(decorated),
		RefsFilter: git.DecorationFilterText(decorated),
		Message:    "tidy up",
		Author:     "Ada",
	}

	if strings.Contains(item.Refs, "origin/") {
		t.Fatalf("Refs = %q, want the duplicate remote entry collapsed away", item.Refs)
	}
	if !strings.Contains(item.FilterValue(), "origin/feature-x") {
		t.Errorf("FilterValue = %q, want the remote form to still match", item.FilterValue())
	}
}

func TestFilterValueIgnoresRefsWhenTheSettingIsOff(t *testing.T) {
	withShowRefs(t, false)

	item := GitCommitLogItem{Hash: "abc1234", Refs: "origin/feature-x", RefsFilter: "origin/feature-x", Message: "tidy up", Author: "Ada"}

	if strings.Contains(item.FilterValue(), "origin/feature-x") {
		t.Errorf("FilterValue = %q, want no refs: the rows show none, so a filter must not match them", item.FilterValue())
	}
}

func TestFilterValueDoesNotDependOnTheRowWidth(t *testing.T) {
	withShowRefs(t, true)

	// A panel this narrow draws no ref block at all, but someone filtering by a
	// branch name still wants that branch's commits.
	item := GitCommitLogItem{Hash: "abc1234", Refs: "origin/feature-x", RefsFilter: "origin/feature-x", Message: "tidy up", Author: "Ada"}
	if block := refBlockText(item.Refs, 16-constant.ListItemOrTitleWidthPad); block != "" {
		t.Fatalf("refBlockText = %q, want nothing drawn on a 16-column panel", block)
	}

	if !strings.Contains(item.FilterValue(), "origin/feature-x") {
		t.Errorf("FilterValue = %q, want the branch name even though the row cannot draw it", item.FilterValue())
	}
}

func TestRowDrawsNoRefsWhenTheSettingIsOff(t *testing.T) {
	withShowRefs(t, false)

	row := renderPlainRow(t, GitCommitLogItem{
		Hash:    "abc1234def",
		Refs:    "HEAD -> master",
		Message: "add a thing",
		Author:  "Ada Lovelace",
	}, 120)

	if strings.Contains(row, "[") {
		t.Errorf("row = %q, want no ref block when refs are turned off", row)
	}
}

func TestRefBlockTextIsEmptyForAnUndecoratedCommit(t *testing.T) {
	if block := refBlockText("", 120); block != "" {
		t.Errorf("refBlockText = %q, want nothing drawn for a commit with no refs", block)
	}
}

func TestRefBlockTextBracketsAShortRef(t *testing.T) {
	if block := refBlockText("HEAD -> master", 120); block != "[HEAD -> master]" {
		t.Errorf("refBlockText = %q, want the refs bracketed unchanged", block)
	}
}

func TestRefBlockTextIsDroppedOnANarrowRow(t *testing.T) {
	if block := refBlockText("master", commitLogRefsMinAvailableWidth-1); block != "" {
		t.Errorf("refBlockText = %q, want nothing: a wide lane leaves no room for both refs and a subject", block)
	}
}

func TestRefBlockTextNeverTakesMoreThanHalfTheRow(t *testing.T) {
	longRefs := "HEAD -> feature/a-very-long-branch-name, origin/feature/a-very-long-branch-name, tag: v1.2.3"

	for available := commitLogRefsMinAvailableWidth; available <= 200; available++ {
		block := refBlockText(longRefs, available)
		// The block is followed by one separating space before the subject.
		used := ansi.StringWidth(block) + 1

		if used > available/2 {
			t.Fatalf("at %d columns the ref block took %d, more than half the row", available, used)
		}
		// Half the row is the whole guarantee now: at the 18-column floor that
		// leaves the subject 9, and the compacted block is short enough that the
		// cap rarely binds at all.
		if available-used < available/2 {
			t.Fatalf("at %d columns the subject was left %d columns, less than half the row", available, available-used)
		}
	}
}

// ------------------------------------
//
//	Render one commit row through the delegate exactly as the panel does
//
// ------------------------------------
func renderRow(t *testing.T, item GitCommitLogItem, width int) string {
	t.Helper()

	model := list.New([]list.Item{item}, GitCommitLogItemDelegate{}, width, 10)

	var out strings.Builder
	GitCommitLogItemDelegate{}.Render(&out, model, 0, item)
	return out.String()
}

// ------------------------------------
//
//	Render one commit row with its styling stripped, so an assertion about the
//	row's text is not confused by the escape sequences carrying its colors
//
// ------------------------------------
func renderPlainRow(t *testing.T, item GitCommitLogItem, width int) string {
	t.Helper()

	return ansi.Strip(renderRow(t, item, width))
}

func TestRowShowsTheRefsPointingAtTheCommit(t *testing.T) {
	withShowRefs(t, true)

	row := renderPlainRow(t, GitCommitLogItem{
		Hash:    "abc1234def",
		Refs:    "HEAD -> master",
		Message: "add a thing",
		Author:  "Ada Lovelace",
	}, 120)

	if !strings.Contains(row, "[HEAD -> master]") {
		t.Errorf("row = %q, want the ref block", row)
	}
	if strings.Index(row, "[HEAD -> master]") > strings.Index(row, "add a thing") {
		t.Errorf("row = %q, want the refs before the subject", row)
	}
}

func TestRowShowsADetachedHeadWithoutABranchName(t *testing.T) {
	withShowRefs(t, true)

	row := renderPlainRow(t, GitCommitLogItem{
		Hash:    "abc1234def",
		Refs:    "HEAD",
		Message: "add a thing",
		Author:  "Ada Lovelace",
	}, 120)

	if !strings.Contains(row, "[HEAD]") {
		t.Errorf("row = %q, want a bare HEAD decoration", row)
	}
}

func TestRowDrawsNoEmptyBlockForAnUndecoratedCommit(t *testing.T) {
	withShowRefs(t, true)

	row := renderPlainRow(t, GitCommitLogItem{
		Hash:    "abc1234def",
		Message: "add a thing",
		Author:  "Ada Lovelace",
	}, 120)

	if strings.Contains(row, "[") {
		t.Errorf("row = %q, want no bracket block when the commit has no refs", row)
	}
}

func TestRowIsUnchangedWhenRefsAreTurnedOff(t *testing.T) {
	withShowRefs(t, false)

	item := GitCommitLogItem{
		Hash:    "abc1234def",
		Refs:    "HEAD -> master",
		Message: "add a thing",
		Author:  "Ada Lovelace",
	}
	withRefs := renderRow(t, item, 120)

	item.Refs = ""
	withoutRefs := renderRow(t, item, 120)

	if withRefs != withoutRefs {
		t.Errorf("row with refs off = %q, want it identical to a row that carries none: %q", withRefs, withoutRefs)
	}
}

func TestRowFollowsTheLiveWidth(t *testing.T) {
	withShowRefs(t, true)

	// A resize only calls SetWidth on the existing list without rebuilding it, so
	// the same item has to render correctly at whatever width it is given.
	item := GitCommitLogItem{
		Hash:    "abc1234def",
		Refs:    "HEAD -> master",
		Message: "add a thing",
		Author:  "Ada Lovelace",
	}

	if row := ansi.Strip(renderRow(t, item, 120)); !strings.Contains(row, "[HEAD -> master]") {
		t.Errorf("row = %q, want the refs a wide panel has room for", row)
	}
	if row := ansi.Strip(renderRow(t, item, 24)); strings.Contains(row, "[") {
		t.Errorf("row = %q, want no ref block: a 24-column panel has no room for one", row)
	}
}
