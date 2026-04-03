package tui

import (
	"testing"

	domainconfig "sqlturbo/internal/domain/config"
)

func TestInitialCursorIndex_PrioritizesGlobalAll(t *testing.T) {
	databases := sampleDatabasesForCursor()
	model := newCursorTestModel(databases, map[string]bool{
		"m1": true,
		"m2": true,
		"d1": true,
		"d2": true,
	})

	cursor := model.initialCursorIndex()
	expected := findItemIndex(model.items, func(item selectionItem) bool {
		return item.Kind == selectionAll
	})
	if cursor != expected {
		t.Fatalf("expected cursor at global ALL index %d, got %d", expected, cursor)
	}
}

func TestInitialCursorIndex_PrioritizesDBAllOverGroupAndDetail(t *testing.T) {
	databases := sampleDatabasesForCursor()
	model := newCursorTestModel(databases, map[string]bool{
		"m1": true,
		"m2": true, // make ALL MySQL selected
		"d1": true, // detail selected too, but db ALL should win
	})

	cursor := model.initialCursorIndex()
	expected := findItemIndex(model.items, func(item selectionItem) bool {
		return item.Kind == selectionGroup && item.DBType == "mysql"
	})
	if cursor != expected {
		t.Fatalf("expected cursor at db ALL index %d, got %d", expected, cursor)
	}
}

func TestInitialCursorIndex_PrioritizesGroupOverDetailAndSkipsPartialGroup(t *testing.T) {
	databases := []domainconfig.Database{
		{ID: "m1", DBType: "mysql", Group: "mixed"},
		{ID: "d1", DBType: "dm", Group: "mixed"},
		{ID: "m2", DBType: "mysql", Group: "other"},
	}

	t.Run("group beats detail", func(t *testing.T) {
		model := newCursorTestModel(databases, map[string]bool{
			"m1": true,
			"d1": true, // make GROUP mixed selected
		})

		cursor := model.initialCursorIndex()
		expected := findItemIndex(model.items, func(item selectionItem) bool {
			return item.Kind == selectionGroup && item.Group == "mixed"
		})
		if cursor != expected {
			t.Fatalf("expected cursor at group index %d, got %d", expected, cursor)
		}
	})

	t.Run("partial group falls back to first detail", func(t *testing.T) {
		model := newCursorTestModel(databases, map[string]bool{
			"m1": true, // partial mixed, group not fully selected
		})

		cursor := model.initialCursorIndex()
		expected := findItemIndex(model.items, func(item selectionItem) bool {
			return item.Kind == selectionSingle && item.DatabaseID == "m1"
		})
		if cursor != expected {
			t.Fatalf("expected cursor at first selected detail index %d, got %d", expected, cursor)
		}
	})
}

func sampleDatabasesForCursor() []domainconfig.Database {
	return []domainconfig.Database{
		{ID: "m1", DBType: "mysql", Group: "g1"},
		{ID: "m2", DBType: "mysql", Group: "g1"},
		{ID: "d1", DBType: "dm", Group: "g2"},
		{ID: "d2", DBType: "dm", Group: "g2"},
	}
}

func newCursorTestModel(databases []domainconfig.Database, selected map[string]bool) selectorModel {
	return selectorModel{
		items:     buildSelectionItems(databases),
		databases: databases,
		selected:  selected,
	}
}

func findItemIndex(items []selectionItem, match func(selectionItem) bool) int {
	for index, item := range items {
		if match(item) {
			return index
		}
	}
	return -1
}
