package branch

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/style"
	"github.com/gohyuhan/gitti/tui/utils"
)

// ------------------------------------
//
//	GitBranchItem holds a canonical branch name and its display-only checkout
//	status. GitBranchItemDelegate implements list.ItemDelegate, rendering "*"
//	for the current worktree and "+" for a linked-worktree checkout.
//
// ------------------------------------
type (
	GitBranchItemDelegate struct{}
	GitBranchItem         struct {
		BranchName                   string
		IsCheckedOut                 bool
		IsCheckedOutInLinkedWorktree bool
	}
)

func (i GitBranchItem) FilterValue() string {
	return i.BranchName
}

func (d GitBranchItemDelegate) Height() int                             { return 1 }
func (d GitBranchItemDelegate) Spacing() int                            { return 0 }
func (d GitBranchItemDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d GitBranchItemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(GitBranchItem)
	if !ok {
		return
	}

	marker := " "
	if i.IsCheckedOut {
		marker = "*"
	} else if i.IsCheckedOutInLinkedWorktree {
		marker = "+"
	}
	str := fmt.Sprintf(" %s %s", marker, i.BranchName)

	componentWidth := m.Width() - constant.ListItemOrTitleWidthPad

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
	str = utils.TruncateString(str, componentWidth)

	fmt.Fprint(w, fn(str))
}
