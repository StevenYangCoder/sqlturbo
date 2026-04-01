package config

import (
	"fmt"
	"slices"
	"strings"
)

// SupportedDBTypes 定义当前工具支持的数据库类型。
var SupportedDBTypes = []string{"mysql", "dm"}

// AppConfig 是应用根配置，直接映射 YAML 顶层结构。
type AppConfig struct {
	Application Application `yaml:"application"`
}

// Application 定义程序运行所需的全局配置。
type Application struct {
	WaitTime    int        `yaml:"wait_time"`
	RetryTimes  int        `yaml:"retry_times"`
	Concurrency int        `yaml:"concurrency"`
	Databases   []Database `yaml:"sql_turbo"`
}

// Database 定义单个数据库执行节点的全部信息。
type Database struct {
	ID          string `yaml:"id"`
	Group       string `yaml:"group"`
	DBType      string `yaml:"db_type"`
	DBHost      string `yaml:"db_host"`
	DBPort      int    `yaml:"db_port"`
	DBUsername  string `yaml:"db_username"`
	DBPassword  string `yaml:"db_password"`
	DBSchema    string `yaml:"db_schema"`
	SSHHost     string `yaml:"ssh_host"`
	SSHPort     int    `yaml:"ssh_port"`
	SSHUsername string `yaml:"ssh_username"`
	SSHPassword string `yaml:"ssh_password"`
	WorkPath    string `yaml:"work_path"`
	EnvPath     string `yaml:"env_path"`
}

// Normalize 会修正默认值，保证后续流程使用统一的配置语义。
func (c AppConfig) Normalize() AppConfig {
	c.Application = c.Application.Normalize()
	return c
}

// Validate 会检查配置完整性，避免程序启动后才暴露配置问题。
func (c AppConfig) Validate() error {
	return c.Application.Validate()
}

// Normalize 会修正等待与重试等默认值，并标准化每个数据库配置。
func (a Application) Normalize() Application {
	if a.WaitTime < 1 {
		a.WaitTime = 1
	}
	if a.RetryTimes < 0 {
		a.RetryTimes = 0
	}
	if a.Concurrency <= 0 {
		a.Concurrency = len(a.Databases)
	}

	for index := range a.Databases {
		a.Databases[index] = a.Databases[index].Normalize()
	}

	return a
}

// Validate 会校验所有数据库配置与全局参数。
func (a Application) Validate() error {
	if len(a.Databases) == 0 {
		return fmt.Errorf("application.sql_turbo 不能为空")
	}

	seen := make(map[string]struct{}, len(a.Databases))
	for _, database := range a.Databases {
		if err := database.Validate(); err != nil {
			return err
		}
		if _, ok := seen[database.ID]; ok {
			return fmt.Errorf("数据库ID重复：%s", database.ID)
		}
		seen[database.ID] = struct{}{}
	}

	return nil
}

// FilterSelected 会按照原始配置顺序返回被选中的数据库。
func (a Application) FilterSelected(selectedIDs []string) []Database {
	selected := make(map[string]struct{}, len(selectedIDs))
	for _, id := range selectedIDs {
		selected[id] = struct{}{}
	}

	filtered := make([]Database, 0, len(selectedIDs))
	for _, database := range a.Databases {
		if _, ok := selected[database.ID]; ok {
			filtered = append(filtered, database)
		}
	}

	return filtered
}

// Normalize 会修正单库配置中的默认值与格式。
func (d Database) Normalize() Database {
	d.ID = strings.TrimSpace(d.ID)
	d.Group = strings.TrimSpace(d.Group)
	d.DBType = strings.ToLower(strings.TrimSpace(d.DBType))
	d.DBHost = strings.TrimSpace(d.DBHost)
	d.DBUsername = strings.TrimSpace(d.DBUsername)
	d.DBSchema = strings.TrimSpace(d.DBSchema)
	d.SSHHost = strings.TrimSpace(d.SSHHost)
	d.SSHUsername = strings.TrimSpace(d.SSHUsername)
	d.WorkPath = strings.TrimSpace(d.WorkPath)
	d.EnvPath = strings.TrimSpace(d.EnvPath)

	if d.SSHPort <= 0 {
		d.SSHPort = 22
	}

	return d
}

// Validate 会校验单个数据库节点配置是否合法。
func (d Database) Validate() error {
	switch {
	case d.ID == "":
		return fmt.Errorf("数据库ID不能为空")
	case !slices.Contains(SupportedDBTypes, d.DBType):
		return fmt.Errorf("数据库[%s]类型不支持：%s", d.ID, d.DBType)
	case d.DBHost == "":
		return fmt.Errorf("数据库[%s]的db_host不能为空", d.ID)
	case d.DBPort <= 0:
		return fmt.Errorf("数据库[%s]的db_port必须大于0", d.ID)
	case d.DBUsername == "":
		return fmt.Errorf("数据库[%s]的db_username不能为空", d.ID)
	case d.DBPassword == "":
		return fmt.Errorf("数据库[%s]的db_password不能为空", d.ID)
	case d.DBSchema == "":
		return fmt.Errorf("数据库[%s]的db_schema不能为空", d.ID)
	case d.SSHHost == "":
		return fmt.Errorf("数据库[%s]的ssh_host不能为空", d.ID)
	case d.SSHPassword == "":
		return fmt.Errorf("数据库[%s]的ssh_password不能为空", d.ID)
	case d.WorkPath == "":
		return fmt.Errorf("数据库[%s]的work_path不能为空", d.ID)
	default:
		return nil
	}
}

// SSHUser 返回本次 SSH 连接使用的用户名。
func (d Database) SSHUser() string {
	if d.SSHUsername != "" {
		return d.SSHUsername
	}
	if d.DBUsername != "" {
		return d.DBUsername
	}
	return "root"
}

// ShellScriptName 返回不同数据库对应的远程执行脚本名称。
func (d Database) ShellScriptName() (string, error) {
	switch d.DBType {
	case "mysql":
		return "sql_execute_mysql.sh", nil
	case "dm":
		return "sql_execute_dm.sh", nil
	default:
		return "", fmt.Errorf("数据库[%s]暂不支持脚本映射：%s", d.ID, d.DBType)
	}
}

// ProfileDirectory 返回数据库类型对应的策略脚本目录名。
func (d Database) ProfileDirectory() string {
	return d.DBType
}

// RuntimePathDirectory 返回执行脚本前需要追加到 PATH 的远程目录。
func (d Database) RuntimePathDirectory() string {
	return d.EnvPath
}

// CommandArgs 返回远程脚本执行时的命令行参数。
func (d Database) CommandArgs() []string {
	return []string{
		fmt.Sprintf("-ip=%s", d.DBHost),
		fmt.Sprintf("-port=%d", d.DBPort),
		fmt.Sprintf("-user=%s", d.DBUsername),
		fmt.Sprintf("-password=%s", d.DBPassword),
		fmt.Sprintf("-schema=%s", d.DBSchema),
	}
}
