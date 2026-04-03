package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"sqlturbo/internal/application/executor"
	domainhistory "sqlturbo/internal/domain/history"
	domainruntime "sqlturbo/internal/domain/runtime"
	infraAssets "sqlturbo/internal/infrastructure/assets"
	infraConfig "sqlturbo/internal/infrastructure/config"
	"sqlturbo/internal/infrastructure/logging"
	"sqlturbo/internal/infrastructure/system"
	"sqlturbo/internal/presentation/tui"
	"sqlturbo/internal/version"
)

// hostSnapshotResult 保存异步采集主机信息的结果。
type hostSnapshotResult struct {
	snapshot domainhistory.Snapshot
	err      error
}

// Run 负责串联配置加载、交互选择和执行流程。
func Run(ctx context.Context) error {
	// 先定位可执行文件目录，确保后续按相对路径读取 data/logs。
	rootDir, err := executableDir()
	if err != nil {
		return err
	}

	// 确保运行时模板和目录存在。
	createdFiles, err := infraAssets.EnsureRuntimeData(rootDir)
	if err != nil {
		return err
	}

	// 初始化日志管理器。
	logManager, err := logging.NewManager(rootDir)
	if err != nil {
		return err
	}
	defer logManager.Close()

	logger := logManager.Logger()
	logger.Info("程序启动", "版本号", version.Version, "构建时间", version.BuildTime)
	if len(createdFiles) > 0 {
		logger.Info("首次初始化运行目录", "文件", createdFiles)
	}

	// 加载 YAML 配置。
	appConfig, configPath, err := infraConfig.LoadAppConfig(rootDir)
	if err != nil {
		logger.Error("加载配置失败", "错误", err)
		return err
	}
	logger.Info("配置文件加载完成", "路径", configPath)

	// 尝试读取上次保存的 history。
	historyPath := filepath.Join(rootDir, "data", "history")
	previousSnapshot, err := domainhistory.ReadSnapshot(historyPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("读取 history 失败，将按首次执行处理", "错误", err)
	}

	// 异步采集主机信息，避免阻塞交互选择界面。
	snapshotCh := make(chan hostSnapshotResult, 1)
	go func() {
		snapshot, collectErr := system.CollectHostSnapshot(ctx, logger)
		snapshotCh <- hostSnapshotResult{
			snapshot: snapshot,
			err:      collectErr,
		}
	}()

	// 让用户选择这次要执行的数据库。
	selectedIDs, err := tui.RunSelector(appConfig.Application.Databases, previousSnapshot.SelectedIDs)
	if err != nil {
		logger.Error("选择数据库失败", "错误", err)
		return err
	}

	selectedDatabases := appConfig.Application.FilterSelected(selectedIDs)
	for _, database := range selectedDatabases {
		logger.Info("数据库加入执行队列", slog.String("数据库ID", database.ID))
	}

	// 先打开执行详情界面，再在 runner 中等待主机信息采集完成。
	runErr, err := tui.RunDashboard(ctx, selectedDatabases, func(ctx context.Context, notify func(domainruntime.StatusUpdate)) error {
		result := <-snapshotCh
		snapshot := result.snapshot
		if result.err != nil {
			logger.Warn("采集主机信息失败，继续使用可用数据", "错误", result.err)
		}

		// 记录本次选择结果，便于下次默认选中。
		snapshot.SelectedIDs = selectedIDs
		if writeErr := domainhistory.WriteSnapshot(historyPath, snapshot); writeErr != nil {
			logger.Error("写入 history 失败", "错误", writeErr)
			return writeErr
		}

		service := executor.NewService(rootDir, appConfig.Application, snapshot, logger)
		return service.Run(ctx, selectedDatabases, notify)
	})
	if err != nil {
		logger.Error("运行界面失败", "错误", err)
		return err
	}

	if runErr != nil {
		logger.Error("数据库执行失败", "错误", runErr)
		return runErr
	}

	logger.Info("所有数据库执行完成")
	return nil
}

// executableDir 返回当前可执行文件所在目录。
func executableDir() (string, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return "", err
	}

	resolvedPath, err := filepath.EvalSymlinks(executablePath)
	if err == nil {
		executablePath = resolvedPath
	}

	return filepath.Dir(executablePath), nil
}
