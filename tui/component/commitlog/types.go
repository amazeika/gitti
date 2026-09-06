package commitlog

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/gohyuhan/gitti/settings"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/style"
)

const (
	// Below this much room after the hash, monogram and lane, a ref block would
	// leave the subject unreadable, so the row drops the refs instead. Set so that
	// the half-width budget below still clears the shortest useful block: 18/2
	// less the overhead leaves 6 columns, which holds a short branch name and its
	// markers. Compacted refs need a few columns where git's raw list needed
	// tens, so the floor that list required would now hide refs on ample rows.
	commitLogRefsMinAvailableWidth = 18
	// Width taken by the block's own brackets and the space separating it from
	// the subject.
	commitLogRefsBlockOverhead = 3
)

// ------------------------------------
//
//	Cell holds a single commit-graph character and its associated color ID for
//	lane rendering. GitCommitLogItem holds the hash, parents, refs, message,
//	author, graph lane data, and color ID for one commit entry.
//	GitCommitLogItemDelegate renders each row as a 7-char yellow hash, author
//	monogram, colored graph lane, the commit's ref decorations, and truncated
//	commit message.
//
// ------------------------------------
type Cell struct {
	Char    rune
	ColorID int
}

type (
	GitCommitLogItemDelegate struct{}
	GitCommitLogItem         struct {
		Hash    string
		Parents []string
		// Refs is already compacted for display. RefsFilter carries every name in
		// the form git would have printed, including the remote duplicates the
		// compaction drops, so filtering is not narrowed by how a row is drawn.
		Refs       string
		RefsFilter string
		// BranchLabel names the one branch a commit sits on, for callers that need
		// a single branch rather than the whole list.
		BranchLabel  string
		Message      string
		Author       string
		LaneCharList []Cell
		ColorID      int
	}
)

func (i GitCommitLogItem) FilterValue() string {
	filterValue := i.Hash + " " + i.Message + " " + i.Author
	// The setting alone decides this, not the row width. A narrow row has no room
	// to draw refs, but someone filtering by a branch name still wants that
	// branch's commits. Gating on width instead would return nothing at all on
	// the panel sizes where no row can draw refs.
	if settings.GITTICONFIGSETTINGS.CommitLogShowRefs && i.RefsFilter != "" {
		filterValue += " " + i.RefsFilter
	}
	return filterValue
}

// ------------------------------------
//
//	Build the bracketed ref decoration drawn between the commit lane and the
//	subject, given the width left on the row for both. The refs arrive already
//	compacted. The block takes at most half of that width so the subject keeps the
//	rest, and is dropped entirely on a row too narrow to carry both
//
// ------------------------------------
func refBlockText(refs string, availableWidth int) string {
	if refs == "" || availableWidth < commitLogRefsMinAvailableWidth {
		return ""
	}

	budget := availableWidth/2 - commitLogRefsBlockOverhead
	return "[" + ansi.Truncate(refs, budget, "...") + "]"
}

func (d GitCommitLogItemDelegate) Height() int                             { return 1 }
func (d GitCommitLogItemDelegate) Spacing() int                            { return 0 }
func (d GitCommitLogItemDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d GitCommitLogItemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(GitCommitLogItem)
	if !ok {
		return
	}

	var sb strings.Builder
	sb.Grow(3)
	for idx, part := range strings.Fields(i.Author) {
		if idx > 2 {
			break
		}
		for _, r := range part {
			sb.WriteRune(r)
			break
		}
	}
	nameShortForm := sb.String()

	var commitGraphLine strings.Builder

	for _, char := range i.LaneCharList {
		commitGraphLine.WriteString(
			style.NewStyle.Foreground(style.GetColor(char.ColorID)).Render(string(char.Char)),
		)
	}

	componentWidth := m.Width() - constant.ListItemOrTitleWidthPad

	var lineBuilder strings.Builder
	lineBuilder.WriteString(style.NewStyle.Foreground(style.ColorYellowWarm).Render(i.Hash[:7]))
	lineBuilder.WriteString(" ")
	lineBuilder.WriteString(style.NewStyle.Foreground(style.GetColor(i.ColorID)).Render(fmt.Sprintf("%-*s", 3, nameShortForm)))
	lineBuilder.WriteString(" ")
	lineBuilder.WriteString(commitGraphLine.String())
	lineBuilder.WriteString(" ")

	// Resolved against the live width: a resize only calls SetWidth on the
	// existing list, so anything cached at build time would be sized for a width
	// the panel no longer has.
	if settings.GITTICONFIGSETTINGS.CommitLogShowRefs {
		if refBlock := refBlockText(i.Refs, componentWidth-lipgloss.Width(lineBuilder.String())); refBlock != "" {
			lineBuilder.WriteString(style.NewStyle.Foreground(style.GetColor(i.ColorID)).Bold(true).Render(refBlock))
			lineBuilder.WriteString(" ")
		}
	}

	lineBuilder.WriteString(style.NewStyle.Render(i.Message))

	strContent := lineBuilder.String()

	needTruncate := false

	if lipgloss.Width(strContent) > componentWidth {
		needTruncate = true
		componentWidth -= 3
	}

	var fn func(...string) string
	if index == m.Index() {
		fn = func(s ...string) string {
			return style.SelectedItemStyle.Render("❯ " + strings.Join(s, " "))
		}
	} else {
		fn = func(s ...string) string {
			return style.ItemStyle.Render("  " + strings.Join(s, " "))
		}
	}

	str := style.NewStyle.MaxWidth(componentWidth).Render(strContent)

	if needTruncate {
		str += "..."
	}

	fmt.Fprint(w, fn(str))
}
