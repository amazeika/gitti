package commitlog

import (
	"testing"

	"charm.land/bubbles/v2/list"
)

// ------------------------------------
//
//	Build a list of commit rows carrying only the fields the selection recovery
//	depends on
//
// ------------------------------------
func commitItems(hashes ...string) []list.Item {
	items := make([]list.Item, 0, len(hashes))
	for _, hash := range hashes {
		items = append(items, GitCommitLogItem{Hash: hash, Message: "a commit", Author: "Ada"})
	}
	return items
}

func TestPositionOfCommitFindsTheSelectedCommit(t *testing.T) {
	if position := positionOfCommit(commitItems("aaa", "bbb", "ccc"), "bbb"); position != 1 {
		t.Errorf("position = %d, want 1", position)
	}
}

func TestPositionOfCommitSurvivesRefsChangingOnTheSelectedCommit(t *testing.T) {
	// The refs on a selected commit change on an ordinary refresh, which is what
	// breaks a recovery keyed on the filter value. The hash does not move.
	items := []list.Item{
		GitCommitLogItem{Hash: "aaa", Refs: "HEAD -> master", Message: "a commit", Author: "Ada"},
		GitCommitLogItem{Hash: "bbb", Refs: "origin/master, tag: v1", Message: "a commit", Author: "Ada"},
	}

	if position := positionOfCommit(items, "bbb"); position != 1 {
		t.Errorf("position = %d, want 1: a refresh must not move the cursor to another commit", position)
	}
}

func TestPositionOfCommitReportsAbsentWhenTheCommitIsGone(t *testing.T) {
	// Reporting a position here would let the caller mistake another commit for
	// the one that was selected.
	if position := positionOfCommit(commitItems("aaa", "bbb"), "zzz"); position != -1 {
		t.Errorf("position = %d, want -1", position)
	}
}

func TestPositionOfCommitReportsAbsentWithoutAPreviousSelection(t *testing.T) {
	if position := positionOfCommit(commitItems("aaa", "bbb"), ""); position != -1 {
		t.Errorf("position = %d, want -1", position)
	}
}

func TestSelectionIndexKeepsTheCommitItFound(t *testing.T) {
	if index := selectionIndex(10, 4, true, 7); index != 4 {
		t.Errorf("index = %d, want the position the commit was found at", index)
	}
}

func TestSelectionIndexLeavesTheRememberedRowWithoutAPreviousSelection(t *testing.T) {
	if index := selectionIndex(10, -1, false, 7); index != 7 {
		t.Errorf("index = %d, want the remembered position", index)
	}
}

func TestSelectionIndexClampsARememberedRowPastTheEnd(t *testing.T) {
	if index := selectionIndex(3, -1, false, 7); index != 2 {
		t.Errorf("index = %d, want the last row", index)
	}
}

func TestSelectionIndexAbandonsTheRememberedRowWhenTheSelectedCommitIsGone(t *testing.T) {
	// A filter keyed on ref text drops the selected commit as soon as its refs
	// move. Reusing the remembered position would put the cursor on whatever now
	// occupies that row, and reset, revert and tag act on the selection.
	if index := selectionIndex(10, -1, true, 7); index != 0 {
		t.Errorf("index = %d, want the top of the list rather than a stale position", index)
	}
}

func TestSelectionIndexClampsANegativeRememberedRow(t *testing.T) {
	if index := selectionIndex(10, -1, false, -3); index != 0 {
		t.Errorf("index = %d, want the first row", index)
	}
}
