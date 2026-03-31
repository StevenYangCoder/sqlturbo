package tui

import (
	"context"
	"fmt"
	"strings"

	domainconfig "sqlturbo/internal/domain/config"
	domainruntime "sqlturbo/internal/domain/runtime"

	tea "github.com/charmbracelet/bubbletea"
)

// RunnerFunc 定义执行层向界面层暴露的统一运行入口。
type RunnerFunc func(ctx context.Context, notify func(domainruntime.StatusUpdate)) error

// statusMsg 是执行层投递给 Bubble Tea 的实时消息。
type statusMsg struct {
	status domainruntime.StatusUpdate
}

// finishedMsg 表示所有数据库任务都已经结束。
type finishedMsg struct {
	runErr error
}

// dashboardModel 是运行态 TUI 的页面模型。
type dashboardModel struct {
	order  []string
	status map[string]domainruntime.StatusUpdate
	done   bool
	runErr error
}

// RunDashboard 会启动运行态界面，并在全部结束后等待用户回车关闭。
func RunDashboard(ctx context.Context, databases []domainconfig.Database, runner RunnerFunc) (error, error) {
	model := dashboardModel{
		order:  make([]string, 0, len(databases)),
		status: make(map[string]domainruntime.StatusUpdate, len(databases)),
	}

	for _, database := range databases {
		model.order = append(model.order, database.ID)
		model.status[database.ID] = domainruntime.StatusUpdate{
			DatabaseID: database.ID,
			Step:       domainruntime.StepPending,
			Message:    "等待执行",
			Progress:   "-",
		}
	}

	program := tea.NewProgram(model)

	go func() {
		runErr := runner(ctx, func(update domainruntime.StatusUpdate) {
			program.Send(statusMsg{status: update})
		})
		program.Send(finishedMsg{runErr: runErr})
	}()

	finalModel, err := program.Run()
	if err != nil {
		return nil, err
	}

	result := finalModel.(dashboardModel)
	return result.runErr, nil
}

// Init 是 Bubble Tea 模型初始化入口，此处无需异步命令。
func (m dashboardModel) Init() tea.Cmd {
	return nil
}

// Update 负责接收执行状态和用户关闭动作。
func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case statusMsg:
		m.status[message.status.DatabaseID] = message.status
	case finishedMsg:
		m.done = true
		m.runErr = message.runErr
	case tea.KeyMsg:
		switch message.String() {
		case "enter":
			if m.done {
				return m, tea.Quit
			}
		}
	}

	return m, nil
}

// View 会实时渲染每个数据库的执行阶段、信息和进度。
func (m dashboardModel) View() string {
	var builder strings.Builder

	builder.WriteString("\n数据库执行中，界面会实时刷新当前进度。\n")
	builder.WriteString("执行详情如下：\n\n")

	for _, id := range m.order {
		status := m.status[id]
		builder.WriteString(fmt.Sprintf(
			"%s......%s......%s......%s\n",
			id,
			emptyFallback(string(status.Step)),
			emptyFallback(status.Message),
			emptyFallback(status.Progress),
		))
	}

	if m.done {
		builder.WriteString("\n")
		if m.runErr != nil {
			builder.WriteString("存在执行失败任务，详情请查看 logs 下日志。\n")
		}
		builder.WriteString("按 Enter 关闭当前终端。\n")
	}

	return builder.String()
}

// emptyFallback 用于避免界面上出现空白字段。
func emptyFallback(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
