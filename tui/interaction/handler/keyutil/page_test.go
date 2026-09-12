package keyutil

import (
	"fmt"
	"io"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/tui/constant"
	keybindingPopUp "github.com/gohyuhan/gitti/tui/popup/keybinding"
	remotePopUp "github.com/gohyuhan/gitti/tui/popup/remote"
	"github.com/gohyuhan/gitti/tui/types"
)

type pageTestItem string

func (i pageTestItem) FilterValue() string { return string(i) }

type pageTestDelegate struct{}

func (pageTestDelegate) Height() int                                  { return 1 }
func (pageTestDelegate) Spacing() int                                 { return 0 }
func (pageTestDelegate) Update(tea.Msg, *list.Model) tea.Cmd          { return nil }
func (pageTestDelegate) Render(io.Writer, list.Model, int, list.Item) {}

func TestMoveListSelectionByPage(t *testing.T) {
	items := make([]list.Item, 12)
	for i := range items {
		items[i] = pageTestItem(fmt.Sprint(i))
	}
	model := list.New(items, pageTestDelegate{}, 40, 10)

	if index, changed := MoveListSelectionByPage(&model, 1, 5); index != 5 || !changed {
		t.Fatalf("page down = (%d, %t), want (5, true)", index, changed)
	}
	if index, changed := MoveListSelectionByPage(&model, 1, 5); index != 10 || !changed {
		t.Fatalf("second page down = (%d, %t), want (10, true)", index, changed)
	}
	if index, changed := MoveListSelectionByPage(&model, 1, 5); index != 11 || !changed {
		t.Fatalf("clamped page down = (%d, %t), want (11, true)", index, changed)
	}
	if index, changed := MoveListSelectionByPage(&model, 1, 5); index != 11 || changed {
		t.Fatalf("page down at end = (%d, %t), want (11, false)", index, changed)
	}
	if index, changed := MoveListSelectionByPage(&model, -1, 5); index != 6 || !changed {
		t.Fatalf("page up = (%d, %t), want (6, true)", index, changed)
	}
}

func TestMoveListSelectionByPageUsesLivePaginatorSize(t *testing.T) {
	items := make([]list.Item, 20)
	for i := range items {
		items[i] = pageTestItem(fmt.Sprint(i))
	}
	model := list.New(items, pageTestDelegate{}, 40, 8)
	model.SetShowTitle(false)
	model.SetShowFilter(false)
	model.SetShowStatusBar(false)
	model.SetShowPagination(false)
	model.SetShowHelp(false)

	if model.Paginator.PerPage != 8 {
		t.Fatalf("per-page size = %d, want 8", model.Paginator.PerPage)
	}
	if index, _ := MoveListSelectionByPage(&model, 1, 0); index != 8 {
		t.Fatalf("page down before resize = %d, want 8", index)
	}

	model.SetHeight(4)
	if index, _ := MoveListSelectionByPage(&model, 1, 0); index != 12 {
		t.Fatalf("page down after resize = %d, want 12", index)
	}
}

func TestMoveListSelectionByPageHandlesEmptyList(t *testing.T) {
	model := list.New(nil, pageTestDelegate{}, 40, 10)
	if index, changed := MoveListSelectionByPage(&model, 1, 5); index != 0 || changed {
		t.Fatalf("empty list = (%d, %t), want (0, false)", index, changed)
	}
}

func TestPageKeyPressMsgUpdateForPopupList(t *testing.T) {
	items := make([]list.Item, 20)
	for i := range items {
		items[i] = pageTestItem(fmt.Sprint(i))
	}
	popupList := list.New(items, pageTestDelegate{}, 40, 6)
	popupList.SetShowTitle(false)
	popupList.SetShowFilter(false)
	popupList.SetShowStatusBar(false)
	popupList.SetShowPagination(false)
	popupList.SetShowHelp(false)
	m := &types.GittiModel{
		PopUpType:  constant.ChooseRemotePopUp,
		PopUpModel: &remotePopUp.ChooseRemotePopUpModel{RemoteList: popupList},
	}

	PageKeyPressMsgUpdateForPopUp(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}), m)
	if got := m.PopUpModel.(*remotePopUp.ChooseRemotePopUpModel).RemoteList.Index(); got != 6 {
		t.Fatalf("popup page down index = %d, want 6", got)
	}
	PageKeyPressMsgUpdateForPopUp(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}), m)
	if got := m.PopUpModel.(*remotePopUp.ChooseRemotePopUpModel).RemoteList.Index(); got != 0 {
		t.Fatalf("popup page up index = %d, want 0", got)
	}
}

func TestPageKeyPressMsgUpdateForPopupViewport(t *testing.T) {
	vp := viewport.New()
	vp.SetHeight(3)
	vp.SetWidth(20)
	vp.SetContent("0\n1\n2\n3\n4\n5\n6")
	m := &types.GittiModel{
		PopUpType:  constant.KeybindingAndFeatureInstructionsPopUp,
		PopUpModel: &keybindingPopUp.KeybindingAndFeatureInstructionsPopUpModel{GlobalKeyBindingViewport: vp},
	}

	PageKeyPressMsgUpdateForPopUp(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgDown}), m)
	if got := m.PopUpModel.(*keybindingPopUp.KeybindingAndFeatureInstructionsPopUpModel).GlobalKeyBindingViewport.YOffset(); got != 3 {
		t.Fatalf("popup viewport page down offset = %d, want 3", got)
	}
}
