package style

import (
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderCommitHashWithRefsShowsTheRefsBesideTheHash(t *testing.T) {
	rendered := ansi.Strip(RenderCommitHashWithRefs("a1b2c3d", "HEAD -> master, origin/master", ColorYellowWarm, 80))

	if !strings.HasPrefix(rendered, "a1b2c3d ") {
		t.Errorf("rendered = %q, want the hash first", rendered)
	}
	if !strings.Contains(rendered, "[HEAD -> master, origin/master]") {
		t.Errorf("rendered = %q, want the refs bracketed after the hash", rendered)
	}
}

func TestRenderCommitHashWithRefsKeepsEachPopupsOwnHashColour(t *testing.T) {
	// Each popup styled its hash differently before this helper existed, and a
	// commit with no refs has to render exactly as that popup's bare hash did.
	for _, hashColor := range []struct {
		name  string
		value color.Color
	}{
		{"the reset popup's purple", ColorPurpleVibrant},
		{"the revert and tag popups' yellow", ColorYellowWarm},
	} {
		bare := NewStyle.Foreground(hashColor.value).Render("a1b2c3d")

		if rendered := RenderCommitHashWithRefs("a1b2c3d", "", hashColor.value, 80); rendered != bare {
			t.Errorf("%s: rendered = %q, want it identical to the bare hash %q", hashColor.name, rendered, bare)
		}
	}
}

func TestRenderCommitHashWithRefsTruncatesALongRefList(t *testing.T) {
	// Every branch, remote and tag pointing at a commit lands in this string, and
	// these popups have no height limit.
	many := strings.Repeat("origin/a-long-branch-name, ", 40)

	rendered := ansi.Strip(RenderCommitHashWithRefs("a1b2c3d", many, ColorYellowWarm, 60))

	if width := ansi.StringWidth(rendered); width > 60 {
		t.Errorf("rendered %d columns wide, want no more than the 60 available", width)
	}
	if !strings.Contains(rendered, "...") {
		t.Errorf("rendered = %q, want the ref list truncated", rendered)
	}
}

func TestRenderCommitHashWithRefsDropsTheRefsWhenThereIsNoRoom(t *testing.T) {
	rendered := RenderCommitHashWithRefs("a1b2c3d", "HEAD -> master", ColorYellowWarm, 12)
	bare := NewStyle.Foreground(ColorYellowWarm).Render("a1b2c3d")

	if rendered != bare {
		t.Errorf("rendered = %q, want the bare hash: brackets around an ellipsis say nothing", rendered)
	}
}

func TestRenderCommitHashWithBareRefsLeavesTheBracketsToTheTemplate(t *testing.T) {
	// The revert title already wraps the identity in brackets of its own, so
	// bracketing the refs again would nest one pair inside another.
	rendered := ansi.Strip(RenderCommitHashWithBareRefs("a1b2c3d", "HEAD -> master", ColorYellowWarm, 80))

	if strings.Contains(rendered, "[") || strings.Contains(rendered, "]") {
		t.Errorf("rendered = %q, want no brackets of its own", rendered)
	}
	if rendered != "a1b2c3d HEAD -> master" {
		t.Errorf("rendered = %q, want the hash and refs separated by a space", rendered)
	}
}

func TestRenderCommitHashWithBareRefsIsJustTheHashForAnUndecoratedCommit(t *testing.T) {
	// Its template renders "commit: [%s]", so a commit with no refs has to come
	// back as the bare hash for that line to read as it did before.
	rendered := RenderCommitHashWithBareRefs("a1b2c3d", "", ColorYellowWarm, 80)
	bare := NewStyle.Foreground(ColorYellowWarm).Render("a1b2c3d")

	if rendered != bare {
		t.Errorf("rendered = %q, want it identical to the bare hash %q", rendered, bare)
	}
}

func TestPopUpValueBudgetSubtractsTheLabelOnTheValuesLine(t *testing.T) {
	// The label in front of the value differs by locale, so a budget taken from
	// the popup width alone overruns by whatever it happens to occupy.
	formatted := "Are you sure?\n commit: [" + PopUpValueMarker + "]"

	budget := PopUpValueBudget(60, formatted, PopUpValueMarker)

	// 60 less the two border columns, less the 11 columns of " commit: []".
	if want := 60 - 2 - 11; budget != want {
		t.Errorf("budget = %d, want %d", budget, want)
	}
}

func TestPopUpValueBudgetFallsBackToTheContentWidth(t *testing.T) {
	if budget := PopUpValueBudget(60, "no marker here", PopUpValueMarker); budget != PopUpContentWidth(60) {
		t.Errorf("budget = %d, want the full content width when the value has no line", budget)
	}
}
