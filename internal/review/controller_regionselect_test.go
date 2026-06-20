package review

import (
	"context"
	"testing"

	"github.com/livetemplate/livetemplate"
)

func regionCtx(action string, data map[string]interface{}) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), action, data)
}

// TestToggleRegionSelect_Flips pins that the "Select region" toggle is a
// pure on/off flip of RegionSelectArmed — off by default so one-finger
// gestures scroll, on so the overlay captures a drag.
func TestToggleRegionSelect_Flips(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{}

	st, err := c.ToggleRegionSelect(st, regionCtx("toggleRegionSelect", nil))
	if err != nil {
		t.Fatalf("ToggleRegionSelect: %v", err)
	}
	if !st.RegionSelectArmed {
		t.Fatalf("expected armed after first toggle")
	}
	st, err = c.ToggleRegionSelect(st, regionCtx("toggleRegionSelect", nil))
	if err != nil {
		t.Fatalf("ToggleRegionSelect: %v", err)
	}
	if st.RegionSelectArmed {
		t.Fatalf("expected disarmed after second toggle")
	}
}

// TestSelectBlock_RangeSideAndDisarm pins the region→line-range contract:
// a drawn region over the rendered HTML or code preview resolves to a
// source line span, anchors the comment to it (round-tripping with the raw
// view + CSV), and disarms the overlay so the composer is reachable. Side
// defaults to "new"; only "old" is honoured for deleted code rows.
func TestSelectBlock_RangeSideAndDisarm(t *testing.T) {
	c := &PrereviewController{}

	t.Run("default side new, disarms", func(t *testing.T) {
		st := PrereviewState{RegionSelectArmed: true}
		st, err := c.SelectBlock(st, regionCtx("selectBlock",
			map[string]interface{}{"from": 12, "to": 18}))
		if err != nil {
			t.Fatalf("SelectBlock: %v", err)
		}
		if st.SelectionAnchor != 12 || st.SelectionEnd != 18 {
			t.Errorf("range = (%d,%d), want (12,18)", st.SelectionAnchor, st.SelectionEnd)
		}
		if st.SelectionSide != "new" {
			t.Errorf("side = %q, want new", st.SelectionSide)
		}
		if st.RegionSelectArmed {
			t.Errorf("expected overlay disarmed after capture")
		}
	})

	t.Run("explicit old side honoured", func(t *testing.T) {
		st := PrereviewState{}
		st, err := c.SelectBlock(st, regionCtx("selectBlock",
			map[string]interface{}{"from": 3, "to": 3, "side": "old"}))
		if err != nil {
			t.Fatalf("SelectBlock: %v", err)
		}
		if st.SelectionSide != "old" {
			t.Errorf("side = %q, want old", st.SelectionSide)
		}
	})

	t.Run("bogus side falls back to new", func(t *testing.T) {
		st := PrereviewState{}
		st, _ = c.SelectBlock(st, regionCtx("selectBlock",
			map[string]interface{}{"from": 1, "to": 1, "side": "garbage"}))
		if st.SelectionSide != "new" {
			t.Errorf("side = %q, want new", st.SelectionSide)
		}
	})

	t.Run("invalid range errors", func(t *testing.T) {
		st := PrereviewState{}
		if _, err := c.SelectBlock(st, regionCtx("selectBlock",
			map[string]interface{}{"from": 0, "to": 5})); err == nil {
			t.Error("expected error for from<=0")
		}
		if _, err := c.SelectBlock(st, regionCtx("selectBlock",
			map[string]interface{}{"from": 9, "to": 4})); err == nil {
			t.Error("expected error for to<from")
		}
	})
}

// TestSelectImageArea_Disarms pins that capturing an image region also
// disarms the overlay (the pixel-rect path, kind=area).
func TestSelectImageArea_Disarms(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{SelectedFile: "logo.png", RegionSelectArmed: true}
	st, err := c.SelectImageArea(st, regionCtx("selectImageArea",
		map[string]interface{}{"x": 0.1, "y": 0.2, "w": 0.3, "h": 0.4}))
	if err != nil {
		t.Fatalf("SelectImageArea: %v", err)
	}
	if st.CommentMode != commentKindArea {
		t.Errorf("CommentMode = %q, want area", st.CommentMode)
	}
	if st.SelectionArea.Empty() {
		t.Errorf("expected a non-empty selection rectangle")
	}
	if st.RegionSelectArmed {
		t.Errorf("expected overlay disarmed after capture")
	}
}

// TestClearSelection_Disarms pins that Cancel/ESC also disarms the
// overlay (so a cancelled selection returns the page to normal scrolling).
func TestClearSelection_Disarms(t *testing.T) {
	c := &PrereviewController{}
	st := PrereviewState{RegionSelectArmed: true, SelectionAnchor: 4, SelectionEnd: 6}
	st, err := c.ClearSelection(st, regionCtx("clearSelection", nil))
	if err != nil {
		t.Fatalf("ClearSelection: %v", err)
	}
	if st.RegionSelectArmed {
		t.Errorf("expected overlay disarmed after ClearSelection")
	}
}
