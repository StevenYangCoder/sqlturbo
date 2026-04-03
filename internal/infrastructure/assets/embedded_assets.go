package assets

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

const runtimeConfigName = "application.yaml"

// ProfileFile 表示内置的数据库策略脚本文件。
type ProfileFile struct {
	Name    string
	Content []byte
}

// embeddedData 保存运行时需要的模板和数据库策略脚本。
//
//go:embed data/application.yaml data/db_profiles/dm/sql_execute_dm.sh data/db_profiles/mysql/sql_execute_mysql.sh
var embeddedData embed.FS

// EnsureRuntimeData 确保运行时需要的数据目录和模板文件存在。
func EnsureRuntimeData(rootDir string) ([]string, error) {
	// data 目录存放模板、history 和策略脚本。
	dataDir := filepath.Join(rootDir, "data")
	// logs 目录用于运行时日志输出。
	logsDir := filepath.Join(rootDir, "logs")

	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败：%w", err)
	}

	created := make([]string, 0, 1)
	runtimeCreated, err := ensureEmbeddedFile(
		filepath.Join(dataDir, runtimeConfigName),
		path.Join("data", runtimeConfigName),
	)
	if err != nil {
		return nil, err
	}
	if runtimeCreated {
		created = append(created, filepath.ToSlash(filepath.Join("data", runtimeConfigName)))
	}

	return created, nil
}

// ensureEmbeddedFile 将内置资源落盘到目标路径。
func ensureEmbeddedFile(target string, embeddedPath string) (bool, error) {
	if _, err := os.Stat(target); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("检查模板文件失败：%w", err)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, fmt.Errorf("创建模板目录失败：%w", err)
	}

	content, err := fs.ReadFile(embeddedData, embeddedPath)
	if err != nil {
		return false, fmt.Errorf("读取内置模板失败：%w", err)
	}
	if err := os.WriteFile(target, content, 0o644); err != nil {
		return false, fmt.Errorf("写入模板文件失败：%w", err)
	}
	return true, nil
}

// ListProfileFiles 返回指定数据库类型对应的内置策略脚本。
func ListProfileFiles(dbType string) ([]ProfileFile, error) {
	filePath, fileName, err := profileScriptPath(dbType)
	if err != nil {
		return nil, err
	}

	content, err := fs.ReadFile(embeddedData, filePath)
	if err != nil {
		return nil, fmt.Errorf("读取内置策略文件失败：%w", err)
	}

	return []ProfileFile{
		{
			Name:    fileName,
			Content: content,
		},
	}, nil
}

// profileScriptPath 把数据库类型映射到对应的脚本路径。
func profileScriptPath(dbType string) (string, string, error) {
	switch dbType {
	case "dm":
		return path.Join("data", "db_profiles", "dm", "sql_execute_dm.sh"), "sql_execute_dm.sh", nil
	case "mysql":
		return path.Join("data", "db_profiles", "mysql", "sql_execute_mysql.sh"), "sql_execute_mysql.sh", nil
	default:
		return "", "", fmt.Errorf("不支持的数据库类型：%s", dbType)
	}
}
