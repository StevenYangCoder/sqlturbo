package tui

import (
	"fmt"
	"strings"
	"time"

	"sqlturbo/internal/version"

	"github.com/mattn/go-runewidth"
)

// welcomeInfoItem 定义启动页中可维护的附加展示项。
type welcomeInfoItem struct {
	Label string
	Value string
}

// maintainedWelcomeInfo 维护除版本号与构建时间外的启动页附加信息。
// Value 为空的条目不会被展示。
var maintainedWelcomeInfo = []welcomeInfoItem{
	{Label: "作者", Value: "暂不透露"},
	{Label: "GitHub", Value: "https://github.com/StevenYangCoder/sqlturbo"},
}

func buildWelcomeBanner() string {
	title := "欢迎使用SQL Turbo"
	lines := []string{
		"版本号：" + version.Version,
		"构建时间：" + formatBuildTime(version.BuildTime),
	}

	for _, item := range maintainedWelcomeInfo {
		if strings.TrimSpace(item.Value) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s：%s", item.Label, item.Value))
	}

	contentWidth := runewidth.StringWidth(title)
	for _, line := range lines {
		if width := runewidth.StringWidth(line); width > contentWidth {
			contentWidth = width
		}
	}
	contentWidth += 2

	border := strings.Repeat("=", contentWidth+4)

	var builder strings.Builder
	builder.WriteString(border)
	builder.WriteString("\n")
	builder.WriteString("==")
	builder.WriteString(centerText(title, contentWidth))
	builder.WriteString("==\n")

	for _, line := range lines {
		builder.WriteString("==")
		builder.WriteString(padRight(line, contentWidth))
		builder.WriteString("==\n")
	}

	builder.WriteString(border)
	builder.WriteString("\n\n")
	return builder.String()
}

func formatBuildTime(value string) string {
	if value == "" {
		return "unknown"
	}

	layouts := []string{time.RFC3339Nano, time.RFC3339}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.Format("2006-01-02 15:04:05")
		}
	}

	return value
}

func centerText(value string, width int) string {
	currentWidth := runewidth.StringWidth(value)
	if currentWidth >= width {
		return value
	}

	padding := width - currentWidth
	left := padding / 2
	right := padding - left
	return strings.Repeat(" ", left) + value + strings.Repeat(" ", right)
}

func padRight(value string, width int) string {
	currentWidth := runewidth.StringWidth(value)
	if currentWidth >= width {
		return value
	}
	return value + strings.Repeat(" ", width-currentWidth)
}
