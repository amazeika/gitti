package nontyping

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	"github.com/gohyuhan/gitti/tui/types"
)

func TestVisibleListRows(t *testing.T) {
	for _, test := range []struct {
		height int
		want   int
	}{{1, 1}, {2, 1}, {9, 7}} {
		if got := visibleListRows(test.height); got != test.want {
			t.Errorf("visibleListRows(%d) = %d, want %d", test.height, got, test.want)
		}
	}
}

func TestPageDetailViewport(t *testing.T) {
	vp := viewport.New()
	vp.SetHeight(3)
	vp.SetWidth(20)
	vp.SetContent(strings.Join([]string{"0", "1", "2", "3", "4", "5", "6", "7"}, "\n"))
	m := &types.GittiModel{DetailPanelViewport: vp}

	pageDetailViewport(m, false, 1)
	if got := m.DetailPanelViewport.YOffset(); got != 3 {
		t.Fatalf("page down offset = %d, want 3", got)
	}
	pageDetailViewport(m, false, -1)
	if got := m.DetailPanelViewport.YOffset(); got != 0 {
		t.Fatalf("page up offset = %d, want 0", got)
	}
}
