package config

import (
	"fmt"
	"os"
	"path/filepath"

	domainconfig "sqlturbo/internal/domain/config"

	"gopkg.in/yaml.v3"
)

// LoadAppConfig 会按优先级读取配置文件，并完成标准化与校验。
func LoadAppConfig(rootDir string) (domainconfig.AppConfig, string, error) {
	candidate := filepath.Join(rootDir, "data", "application.yaml")

	content, err := os.ReadFile(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return domainconfig.AppConfig{}, "", fmt.Errorf("未找到配置文件：data/application.yaml")
		}
		return domainconfig.AppConfig{}, "", fmt.Errorf("读取配置文件失败：%w", err)
	}

	var appConfig domainconfig.AppConfig
	if err := yaml.Unmarshal(content, &appConfig); err != nil {
		return domainconfig.AppConfig{}, "", fmt.Errorf("解析配置文件[%s]失败：%w", candidate, err)
	}

	appConfig = appConfig.Normalize()
	if err := appConfig.Validate(); err != nil {
		return domainconfig.AppConfig{}, "", fmt.Errorf("校验配置文件[%s]失败：%w", candidate, err)
	}

	return appConfig, candidate, nil
}
