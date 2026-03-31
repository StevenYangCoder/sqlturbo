package tui

import (
	"fmt"
	"strings"

	domainconfig "sqlturbo/internal/domain/config"

	tea "github.com/charmbracelet/bubbletea"
)

// selectionKind 区分不同选择项的行为类型。
type selectionKind string

const (
	selectionAll    selectionKind = "all"
	selectionGroup  selectionKind = "group"
	selectionSingle selectionKind = "single"
)

// selectionItem 定义单个可见选择项。
type selectionItem struct {
	Key        string
	Label      string
	Kind       selectionKind
	DBType     string
	Group      string
	DatabaseID string
}

// selectorModel 是数据库选择界面的状态模型。
type selectorModel struct {
	items        []selectionItem
	databases    []domainconfig.Database
	selected     map[string]bool
	cursor       int
	confirmedIDs []string
	cancelled    bool
}

// RunSelector 会展示多选界面，并返回最终确认的数据库ID列表。
func RunSelector(databases []domainconfig.Database, defaultIDs []string) ([]string, error) {
	model := selectorModel{
		items:     buildSelectionItems(databases),
		databases: databases,
		selected:  make(map[string]bool, len(databases)),
	}

	for _, id := range defaultIDs {
		model.selected[id] = true
	}

	program := tea.NewProgram(model)
	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}

	result := finalModel.(selectorModel)
	if result.cancelled {
		return nil, fmt.Errorf("用户取消了数据库选择")
	}
	if len(result.confirmedIDs) == 0 {
		return nil, fmt.Errorf("至少需要选择一个数据库")
	}
	return result.confirmedIDs, nil
}

// Init 是 Bubble Tea 模型初始化方法，此界面不需要额外异步命令。
func (m selectorModel) Init() tea.Cmd {
	return nil
}

// Update 负责处理方向键、空格和回车逻辑。
func (m selectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.KeyMsg:
		switch message.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case " ":
			m.toggleCurrent()
		case "enter":
			selectedIDs := m.selectedDatabaseIDs()
			if len(selectedIDs) == 0 {
				return m, nil
			}
			m.confirmedIDs = selectedIDs
			return m, tea.Quit
		}
	}

	return m, nil
}

// View 用于渲染数据库选择界面。
func (m selectorModel) View() string {
	var builder strings.Builder

	builder.WriteString(buildWelcomeBanner())
	builder.WriteString("Space选择和取消，Enter执行，默认选择上一次执行的数据库\n\n")
	for index, item := range m.items {
		cursor := " "
		if index == m.cursor {
			cursor = ">"
		}

		check := " "
		if m.isItemSelected(item) {
			check = "x"
		}

		builder.WriteString(fmt.Sprintf("%s [%s] %s\n", cursor, check, item.Label))
	}
	return builder.String()
}

// toggleCurrent 会根据当前光标所在项更新选中状态。
func (m *selectorModel) toggleCurrent() {
	item := m.items[m.cursor]

	switch item.Kind {
	case selectionAll:
		target := !m.isItemSelected(item)
		for _, database := range m.databases {
			m.selected[database.ID] = target
		}
	case selectionGroup:
		target := !m.isItemSelected(item)
		for _, database := range m.databases {
			if !matchesGroupItem(database, item) {
				continue
			}
			m.selected[database.ID] = target
		}
	case selectionSingle:
		m.selected[item.DatabaseID] = !m.selected[item.DatabaseID]
	}
}

// isItemSelected 会判断一个条目当前是否处于选中状态。
func (m selectorModel) isItemSelected(item selectionItem) bool {
	switch item.Kind {
	case selectionAll:
		if len(m.databases) == 0 {
			return false
		}
		for _, database := range m.databases {
			if !m.selected[database.ID] {
				return false
			}
		}
		return true
	case selectionGroup:
		found := false
		for _, database := range m.databases {
			if !matchesGroupItem(database, item) {
				continue
			}
			found = true
			if !m.selected[database.ID] {
				return false
			}
		}
		return found
	case selectionSingle:
		return m.selected[item.DatabaseID]
	default:
		return false
	}
}

// selectedDatabaseIDs 会按配置顺序返回当前已选择的数据库ID。
func (m selectorModel) selectedDatabaseIDs() []string {
	result := make([]string, 0, len(m.databases))
	for _, database := range m.databases {
		if m.selected[database.ID] {
			result = append(result, database.ID)
		}
	}
	return result
}

// buildSelectionItems 会按照 README 指定顺序构建 ALL、分组项与数据库明细项。
func buildSelectionItems(databases []domainconfig.Database) []selectionItem {
	items := make([]selectionItem, 0, len(databases)+8)
	if len(databases) >= 2 {
		items = append(items, selectionItem{Key: "ALL", Label: "ALL", Kind: selectionAll})
	}

	typeOrder := []struct {
		dbType string
		label  string
	}{
		{dbType: "mysql", label: "ALL MySQL"},
		{dbType: "dm", label: "ALL Dm"},
		{dbType: "pg", label: "ALL PG"},
	}

	for _, item := range typeOrder {
		count := 0
		for _, database := range databases {
			if database.DBType == item.dbType {
				count++
			}
		}
		if count > 1 {
			items = append(items, selectionItem{
				Key:    item.label,
				Label:  item.label,
				Kind:   selectionGroup,
				DBType: item.dbType,
			})
		}
	}

	groupSeen := make(map[string]struct{})
	for _, database := range databases {
		if database.Group == "" {
			continue
		}
		if _, exists := groupSeen[database.Group]; exists {
			continue
		}
		groupSeen[database.Group] = struct{}{}
		items = append(items, selectionItem{
			Key:   "GROUP:" + database.Group,
			Label: "GROUP " + database.Group,
			Kind:  selectionGroup,
			Group: database.Group,
		})
	}

	for _, database := range databases {
		items = append(items, selectionItem{
			Key:        database.ID,
			Label:      database.ID,
			Kind:       selectionSingle,
			DatabaseID: database.ID,
		})
	}

	return items
}

func matchesGroupItem(database domainconfig.Database, item selectionItem) bool {
	if item.Group != "" {
		return database.Group == item.Group
	}
	if item.DBType != "" {
		return database.DBType == item.DBType
	}
	return false
}
