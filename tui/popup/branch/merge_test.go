package branch

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"github.com/gohyuhan/gitti/api"
	gitapi "github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/tui/types"
)

func TestMergeChooserPreservesLinkedDecorationAndDiscoveryOrder(t *testing.T) {
	i18n.InitGittiLanguageMapping("en")
	root := t.TempDir()
	run := func(gitArgs ...string) {
		t.Helper()
		cmd := exec.Command("git", gitArgs...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Ada Lovelace", "GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada Lovelace", "GIT_COMMITTER_EMAIL=ada@example.com",
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(gitArgs, " "), err, output)
		}
	}
	run("init", "-q", "-b", "master")
	run("config", "user.name", "Ada Lovelace")
	run("config", "user.email", "ada@example.com")
	run("commit", "-q", "--allow-empty", "-m", "base")
	for _, branchName := range []string{"alpha", "linked", "zeta"} {
		run("branch", branchName)
	}
	run("update-ref", "refs/heads/+skill", "HEAD")
	run("worktree", "add", "-q", filepath.Join(t.TempDir(), "linked"), "linked")

	originalExecutor := executor.GittiCmdExecutor
	t.Cleanup(func() { executor.GittiCmdExecutor = originalExecutor })
	executor.InitCmdExecutor(root)
	gitBranch := gitapi.InitGitBranch(nil, false, logging.InitGittiLogging(8, make(chan string, 16), 3))
	gitBranch.GetLatestBranchesInfo()
	model := &types.GittiModel{
		Width:         100,
		GitOperations: &api.GitOperations{GitBranch: gitBranch},
	}

	InitChooseBranchOptionForMergePopUpModel(model)
	popUp := model.PopUpModel.(*ChooseBranchOptionForMergePopUpModel)
	available := mergeChooserItems(popUp.BranchOptionList.Items())
	if got := strings.Join(mergeChooserNames(available), ","); got != "+skill,alpha,linked,zeta" {
		t.Fatalf("initial available names = %v, want canonical discovery order without master", mergeChooserNames(available))
	}
	linked := mergeChooserItemByName(t, available, "linked")
	if !linked.IsCheckedOutInLinkedWorktree {
		t.Error("initial linked item lost linked-worktree status")
	}
	if plusSkill := mergeChooserItemByName(t, available, "+skill"); plusSkill.FilterValue() != "+skill" {
		t.Errorf("literal-plus FilterValue = %q, want canonical +skill", plusSkill.FilterValue())
	}
	assertMergeChooserRendersLinkedMarker(t, linked)

	// Select out of order. Rebuilding walks AllBranches, so the selected panel
	// must use discovery order rather than click order.
	popUp.BranchOptionList.Select(3) // zeta
	UpdateChooseBranchOptionForMergePopUpModel(model)
	popUp = model.PopUpModel.(*ChooseBranchOptionForMergePopUpModel)
	popUp.BranchOptionList.Select(1) // alpha after zeta moves out
	UpdateChooseBranchOptionForMergePopUpModel(model)
	popUp = model.PopUpModel.(*ChooseBranchOptionForMergePopUpModel)
	popUp.BranchOptionList.Select(1) // linked after alpha and zeta move out
	UpdateChooseBranchOptionForMergePopUpModel(model)
	popUp = model.PopUpModel.(*ChooseBranchOptionForMergePopUpModel)

	selected := mergeChooserItems(popUp.SelectedBranchList.Items())
	if got := strings.Join(mergeChooserNames(selected), ","); got != "alpha,linked,zeta" {
		t.Fatalf("selected names = %s, want discovery order", got)
	}
	linked = mergeChooserItemByName(t, selected, "linked")
	if !linked.IsCheckedOutInLinkedWorktree {
		t.Error("rebuilt selected linked item lost linked-worktree status")
	}
	assertMergeChooserRendersLinkedMarker(t, linked)

	// Unselecting rebuilds both panels from canonical names and refreshes status.
	popUp.BranchOptionSectionSelected.Store(false)
	popUp.SelectedBranchSectionSelected.Store(true)
	popUp.SelectedBranchList.Select(1) // linked
	UpdateChooseBranchOptionForMergePopUpModel(model)
	popUp = model.PopUpModel.(*ChooseBranchOptionForMergePopUpModel)
	available = mergeChooserItems(popUp.BranchOptionList.Items())
	linked = mergeChooserItemByName(t, available, "linked")
	if !linked.IsCheckedOutInLinkedWorktree {
		t.Error("rebuilt available linked item lost linked-worktree status")
	}
	assertMergeChooserRendersLinkedMarker(t, linked)
	if got := strings.Join(mergeChooserNames(mergeChooserItems(popUp.SelectedBranchList.Items())), ","); got != "alpha,zeta" {
		t.Errorf("selected names after unselect = %s, want alpha,zeta", got)
	}
}

func mergeChooserItems(items []list.Item) []GitMergeBranchOptionItem {
	result := make([]GitMergeBranchOptionItem, 0, len(items))
	for _, item := range items {
		result = append(result, item.(GitMergeBranchOptionItem))
	}
	return result
}

func mergeChooserNames(items []GitMergeBranchOptionItem) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.BranchName)
	}
	return names
}

func mergeChooserItemByName(t *testing.T, items []GitMergeBranchOptionItem, name string) GitMergeBranchOptionItem {
	t.Helper()
	for _, item := range items {
		if item.BranchName == name {
			return item
		}
	}
	t.Fatalf("missing merge chooser item %q in %v", name, mergeChooserNames(items))
	return GitMergeBranchOptionItem{}
}

func assertMergeChooserRendersLinkedMarker(t *testing.T, item GitMergeBranchOptionItem) {
	t.Helper()
	delegate := GitMergeBranchOptionItemDelegate{}
	model := list.New([]list.Item{item}, delegate, 80, 2)
	var rendered bytes.Buffer
	delegate.Render(&rendered, model, 0, item)
	if !strings.Contains(rendered.String(), "+ linked") {
		t.Errorf("rendered linked chooser row = %q, want + marker", rendered.String())
	}
}
