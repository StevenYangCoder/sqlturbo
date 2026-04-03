package history

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Snapshot 保存本机网络信息和上一次执行时的数据库选择记录。
type Snapshot struct {
	LocalIPv4   []string
	LocalIPv6   []string
	PublicIPv4  []string
	PublicIPv6  []string
	MACs        []string
	SelectedIDs []string
}

// ReadSnapshot 读取 history 文件并解析为 Snapshot。
func ReadSnapshot(path string) (Snapshot, error) {
	// 打开历史文件。
	file, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()

	var snapshot Snapshot
	// 按行读取 history 内容。
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		switch {
		case strings.HasPrefix(key, "ipv4_"):
			snapshot.LocalIPv4 = append(snapshot.LocalIPv4, value)
		case strings.HasPrefix(key, "ipv6_"):
			snapshot.LocalIPv6 = append(snapshot.LocalIPv6, value)
		case strings.HasPrefix(key, "public_ipv4_"):
			snapshot.PublicIPv4 = append(snapshot.PublicIPv4, value)
		case strings.HasPrefix(key, "public_ipv6_"):
			snapshot.PublicIPv6 = append(snapshot.PublicIPv6, value)
		case strings.HasPrefix(key, "mac"):
			snapshot.MACs = append(snapshot.MACs, value)
		case strings.HasPrefix(key, "selected_db_id_"):
			snapshot.SelectedIDs = append(snapshot.SelectedIDs, value)
		}
	}

	if err := scanner.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("读取history失败：%w", err)
	}

	snapshot.Normalize()
	return snapshot, nil
}

// Normalize 去重并规范化 Snapshot 的内部切片。
func (s *Snapshot) Normalize() {
	s.LocalIPv4 = uniqueSorted(s.LocalIPv4)
	s.LocalIPv6 = uniqueSorted(s.LocalIPv6)
	s.PublicIPv4 = uniqueSorted(s.PublicIPv4)
	s.PublicIPv6 = uniqueSorted(s.PublicIPv6)
	s.MACs = uniqueSorted(s.MACs)
	s.SelectedIDs = uniquePreserveOrder(s.SelectedIDs)
}

// WriteSnapshot 将 Snapshot 写回 history 文件。
func WriteSnapshot(path string, snapshot Snapshot) error {
	// 先规范化再写入，保证文件内容稳定。
	snapshot.Normalize()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建history目录失败：%w", err)
	}

	var builder strings.Builder
	appendSeries(&builder, "ipv4_", snapshot.LocalIPv4)
	appendSeries(&builder, "ipv6_", snapshot.LocalIPv6)
	appendSeries(&builder, "public_ipv4_", snapshot.PublicIPv4)
	appendSeries(&builder, "public_ipv6_", snapshot.PublicIPv6)
	appendSeries(&builder, "mac", snapshot.MACs)
	appendSeries(&builder, "selected_db_id_", snapshot.SelectedIDs)

	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		return fmt.Errorf("写入history失败：%w", err)
	}

	return nil
}

// LockContent 返回锁文件中需要写入的主机信息摘要。
func (s Snapshot) LockContent() string {
	s.Normalize()

	var builder strings.Builder
	appendSeries(&builder, "ipv4_", s.LocalIPv4)
	appendSeries(&builder, "ipv6_", s.LocalIPv6)
	appendSeries(&builder, "public_ipv4_", s.PublicIPv4)
	appendSeries(&builder, "public_ipv6_", s.PublicIPv6)
	appendSeries(&builder, "mac", s.MACs)
	return builder.String()
}

// appendSeries 生成 key=value 格式的多行文本。
func appendSeries(builder *strings.Builder, prefix string, values []string) {
	for index, value := range values {
		// history 文件使用从 1 开始的序号。
		builder.WriteString(fmt.Sprintf("%s%d=%s\n", prefix, index+1, value))
	}
}

// uniqueSorted 对值去重后排序，保证输出稳定。
func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}

	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// uniquePreserveOrder 对值去重，但保留首次出现顺序。
func uniquePreserveOrder(values []string) []string {
	set := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := set[value]; ok {
			continue
		}
		set[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
