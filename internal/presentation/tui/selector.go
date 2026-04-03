package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	domainconfig "sqlturbo/internal/domain/config"

	tea "github.com/charmbracelet/bubbletea"
)

// selectionKind 区分不同选择项的行为类型。
type selectionKind string

const (
	selectionAll          selectionKind = "all"
	selectionGroup        selectionKind = "group"
	selectionSingle       selectionKind = "single"
	selectionHeaderIndent               = "  "
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
	listOffset   int
	windowWidth  int
	windowHeight int
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

	// 默认把光标停在“已选中的分组项”上；如果没有，再停在“已选中的详情项”上。
	model.cursor = model.initialCursorIndex()

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
			m.moveCursorVertical(-1)
		case "down", "j":
			m.moveCursorVertical(1)
		case "left", "h":
			m.moveCursorHorizontal(-1)
		case "right", "l":
			m.moveCursorHorizontal(1)
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
	case tea.WindowSizeMsg:
		m.windowWidth = message.Width
		m.windowHeight = message.Height
	}

	m.keepCursorVisible()
	return m, nil
}

func (m *selectorModel) moveCursorVertical(delta int) {
	if len(m.items) == 0 || delta == 0 {
		return
	}
	if !m.isTwoColumnMode() {
		next := m.cursor + delta
		if next < 0 {
			next = 0
		}
		if next >= len(m.items) {
			next = len(m.items) - 1
		}
		m.cursor = next
		return
	}

	leftIndices, rightIndices := m.columnIndices()
	if row := indexOf(leftIndices, m.cursor); row >= 0 {
		nextRow := clamp(row+delta, 0, len(leftIndices)-1)
		m.cursor = leftIndices[nextRow]
		return
	}
	if row := indexOf(rightIndices, m.cursor); row >= 0 {
		nextRow := clamp(row+delta, 0, len(rightIndices)-1)
		m.cursor = rightIndices[nextRow]
		return
	}
}

func (m *selectorModel) moveCursorHorizontal(direction int) {
	if len(m.items) == 0 || direction == 0 || !m.isTwoColumnMode() {
		return
	}

	leftIndices, rightIndices := m.columnIndices()
	if row := indexOf(leftIndices, m.cursor); row >= 0 {
		if direction < 0 || len(rightIndices) == 0 {
			return
		}
		targetRow := row
		if targetRow >= len(rightIndices) {
			targetRow = len(rightIndices) - 1
		}
		m.cursor = rightIndices[targetRow]
		return
	}
	if row := indexOf(rightIndices, m.cursor); row >= 0 {
		if direction > 0 || len(leftIndices) == 0 {
			return
		}
		targetRow := row
		if targetRow >= len(leftIndices) {
			targetRow = len(leftIndices) - 1
		}
		m.cursor = leftIndices[targetRow]
	}
}

// View 用于渲染数据库选择界面。
func (m selectorModel) View() string {
	var builder strings.Builder

	builder.WriteString(buildWelcomeBanner())
	builder.WriteString("Space选择和取消，Enter执行，默认选择上一次执行的数据库\n\n")

	if m.isTwoColumnMode() {
		header, lines := m.renderTwoColumnSection()
		builder.WriteString(header)
		builder.WriteString("\n")
		for _, line := range m.visibleListLines(lines, 1) {
			builder.WriteString(line)
			builder.WriteString("\n")
		}
		return builder.String()
	}

	builder.WriteString(selectionHeaderIndent + "详情如下：\n")
	lines := m.renderSingleColumnLines()
	for _, line := range m.visibleListLines(lines, 1) {
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return builder.String()
}

func (m selectorModel) renderSingleColumnLines() []string {
	lines := make([]string, 0, len(m.items))
	for index, item := range m.items {
		lines = append(lines, m.renderSelectionLine(item, index))
	}
	return lines
}

func (m selectorModel) renderTwoColumnSection() (string, []string) {
	leftLines := make([]string, 0, len(m.items))
	rightLines := make([]string, 0, len(m.items))

	for index, item := range m.items {
		line := m.renderSelectionLine(item, index)
		if item.Kind == selectionSingle {
			leftLines = append(leftLines, line)
			continue
		}
		rightLines = append(rightLines, line)
	}

	leftWidth := 0
	for _, line := range leftLines {
		if width := utf8.RuneCountInString(line); width > leftWidth {
			leftWidth = width
		}
	}

	rowCount := len(leftLines)
	if len(rightLines) > rowCount {
		rowCount = len(rightLines)
	}

	lines := make([]string, 0, rowCount)
	for row := 0; row < rowCount; row++ {
		left := ""
		if row < len(leftLines) {
			left = leftLines[row]
		}
		right := ""
		if row < len(rightLines) {
			right = rightLines[row]
		}

		padding := leftWidth - utf8.RuneCountInString(left)
		if padding < 0 {
			padding = 0
		}
		lines = append(lines, left+strings.Repeat(" ", padding+4)+right)
	}

	leftHeader := selectionHeaderIndent + "详情"
	rightHeader := selectionHeaderIndent + "分组"
	header := leftHeader + strings.Repeat(" ", maxInt(leftWidth-utf8.RuneCountInString(leftHeader)+4, 4)) + rightHeader
	return header, lines
}

func (m selectorModel) isTwoColumnMode() bool {
	return len(m.databases) > 10
}

func (m selectorModel) columnIndices() ([]int, []int) {
	leftIndices := make([]int, 0, len(m.items))
	rightIndices := make([]int, 0, len(m.items))
	for index, item := range m.items {
		if item.Kind == selectionSingle {
			leftIndices = append(leftIndices, index)
			continue
		}
		rightIndices = append(rightIndices, index)
	}
	return leftIndices, rightIndices
}

func (m selectorModel) renderSelectionLine(item selectionItem, index int) string {
	cursor := " "
	if index == m.cursor {
		cursor = ">"
	}

	check := " "
	if m.isItemSelected(item) {
		check = "x"
	}

	return fmt.Sprintf("%s [%s] %s", cursor, check, item.Label)
}

func (m selectorModel) visibleListLines(lines []string, sectionHeaderLines int) []string {
	if m.windowHeight <= 0 {
		return lines
	}

	available := m.listVisibleHeight(sectionHeaderLines)
	if available <= 0 || len(lines) <= available {
		return lines
	}

	start := m.listOffset
	if start < 0 {
		start = 0
	}
	maxStart := len(lines) - available
	if start > maxStart {
		start = maxStart
	}
	end := start + available
	visible := append([]string(nil), lines[start:end]...)
	if end < len(lines) && len(visible) > 0 {
		visible[len(visible)-1] = selectionHeaderIndent + "..."
	}
	return visible
}

func (m *selectorModel) keepCursorVisible() {
	if m.windowHeight <= 0 {
		return
	}

	available := m.listVisibleHeight(1)
	if available <= 0 {
		m.listOffset = 0
		return
	}

	totalRows := m.listRowCount()
	if totalRows <= available {
		m.listOffset = 0
		return
	}

	cursorRow := m.cursorRow()
	if cursorRow < m.listOffset {
		m.listOffset = cursorRow
	} else if cursorRow >= m.listOffset+available {
		m.listOffset = cursorRow - available + 1
	}

	maxOffset := totalRows - available
	if m.listOffset < 0 {
		m.listOffset = 0
	}
	if m.listOffset > maxOffset {
		m.listOffset = maxOffset
	}
}

func (m selectorModel) listVisibleHeight(sectionHeaderLines int) int {
	// 额外预留 1 行，避免首屏因为边界滚动把欢迎框顶行挤掉。
	headerLines := countLines(buildWelcomeBanner()) + 2 + sectionHeaderLines
	return m.windowHeight - headerLines - 1
}

func (m selectorModel) listRowCount() int {
	if !m.isTwoColumnMode() {
		return len(m.items)
	}

	leftIndices, rightIndices := m.columnIndices()
	if len(leftIndices) > len(rightIndices) {
		return len(leftIndices)
	}
	return len(rightIndices)
}

func (m selectorModel) cursorRow() int {
	if !m.isTwoColumnMode() {
		return m.cursor
	}

	leftIndices, rightIndices := m.columnIndices()
	if row := indexOf(leftIndices, m.cursor); row >= 0 {
		return row
	}
	if row := indexOf(rightIndices, m.cursor); row >= 0 {
		return row
	}
	return 0
}

func indexOf(items []int, target int) int {
	for index, item := range items {
		if item == target {
			return index
		}
	}
	return -1
}

func clamp(value int, min int, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func countLines(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n")
}

// initialCursorIndex 会按照“第一个已选择节点”规则定位初始光标。
// 分组优先级为 ALL > DB ALL > GROUP，其次才是详情节点。
func (m selectorModel) initialCursorIndex() int {
	if len(m.items) == 0 {
		return 0
	}

	for index, item := range m.items {
		if item.Kind == selectionAll && m.isItemSelected(item) {
			return index
		}
	}

	for index, item := range m.items {
		if item.Kind == selectionGroup && item.DBType != "" && m.isItemSelected(item) {
			return index
		}
	}

	for index, item := range m.items {
		if item.Kind == selectionGroup && item.Group != "" && m.isItemSelected(item) {
			return index
		}
	}

	for index, item := range m.items {
		if item.Kind == selectionSingle && m.isItemSelected(item) {
			return index
		}
	}

	return 0
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
