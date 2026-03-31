package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	domainconfig "sqlturbo/internal/domain/config"
	"sqlturbo/internal/domain/history"
	domainruntime "sqlturbo/internal/domain/runtime"
	infraAssets "sqlturbo/internal/infrastructure/assets"
	"sqlturbo/internal/infrastructure/remote"
)

// Service 负责把多个数据库的执行流程编排起来。
type Service struct {
	rootDir  string
	app      domainconfig.Application
	snapshot history.Snapshot
	logger   *slog.Logger
}

// NewService 创建执行服务实例。
func NewService(rootDir string, app domainconfig.Application, snapshot history.Snapshot, logger *slog.Logger) *Service {
	return &Service{
		rootDir:  rootDir,
		app:      app,
		snapshot: snapshot,
		logger:   logger,
	}
}

// Run 会并发执行所有已选数据库，并把实时状态持续推送到展示层。
func (s *Service) Run(ctx context.Context, databases []domainconfig.Database, notify func(domainruntime.StatusUpdate)) error {
	var waitGroup sync.WaitGroup
	var mutex sync.Mutex
	failures := make([]string, 0)

	for _, database := range databases {
		waitGroup.Add(1)

		go func(database domainconfig.Database) {
			defer waitGroup.Done()
			if err := s.runOne(ctx, database, notify); err != nil {
				mutex.Lock()
				failures = append(failures, fmt.Sprintf("%s：%v", database.ID, err))
				mutex.Unlock()
			}
		}(database)
	}

	waitGroup.Wait()

	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("以下数据库执行失败：%s", strings.Join(failures, "；"))
	}

	return nil
}

// runOne 执行单个数据库从连接、上传到远程执行的完整生命周期。
func (s *Service) runOne(ctx context.Context, database domainconfig.Database, notify func(domainruntime.StatusUpdate)) error {
	logger := s.logger.With("数据库ID", database.ID)
	emit := func(step domainruntime.Step, message string, progress string, failed bool) {
		notify(domainruntime.StatusUpdate{
			DatabaseID: database.ID,
			Step:       step,
			Message:    message,
			Progress:   progress,
			Failed:     failed,
		})
	}

	emit(domainruntime.StepInitializing, "正在建立远程连接", "-", false)
	client, err := remote.NewClient(database)
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}
	defer client.Close()

	if err := client.EnsureDir(database.WorkPath); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	lockName, lockPath, err := s.acquireLock(ctx, client, database, emit)
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	lockHeld := true
	releaseLock := func() error {
		if !lockHeld {
			return nil
		}
		if err := client.RemoveFile(lockPath); err != nil {
			return err
		}
		lockHeld = false
		return nil
	}
	defer func() {
		if err := releaseLock(); err != nil {
			logger.Error("释放锁失败", "错误", err, "锁文件", lockPath)
		}
	}()

	if err := s.removeRemoteSQLFiles(client, database, emit); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	localSQLFiles, err := s.listLocalSQLFiles()
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}
	if len(localSQLFiles) == 0 {
		err = fmt.Errorf("当前目录下未找到待执行的SQL文件")
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	if err := s.uploadFiles(client, database, localSQLFiles, emit); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	profileFiles, err := infraAssets.ListProfileFiles(database.ProfileDirectory())
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	if err := s.uploadProfileFiles(client, database, profileFiles, emit); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	scriptName, err := database.ShellScriptName()
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	chmodCommand := fmt.Sprintf("cd %s && chmod 555 %s", shellQuote(database.WorkPath), shellQuote(scriptName))
	if err := client.RunCommand(ctx, chmodCommand); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	command := buildExecutionCommand(database, scriptName)
	logger.Info("执行远程脚本命令", "命令", command)

	execErr := s.runRemoteScript(ctx, client, scriptName, command, emit)
	downloadErr := s.downloadRemoteLog(client, database, emit)

	if execErr != nil && downloadErr != nil {
		err = fmt.Errorf("%v；同时下载日志失败：%v", execErr, downloadErr)
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}
	if execErr != nil {
		emit(domainruntime.StepFailed, execErr.Error(), "-", true)
		return execErr
	}
	if downloadErr != nil {
		emit(domainruntime.StepFailed, downloadErr.Error(), "-", true)
		return downloadErr
	}

	emit(domainruntime.StepReleasingLock, "正在释放锁["+lockName+"]", "-", false)
	if err := releaseLock(); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	emit(domainruntime.StepCompleted, "执行完成", "-", false)
	return nil
}

// acquireLock 会通过排他协调文件把“检查锁 + 创建锁”收敛为原子过程。
func (s *Service) acquireLock(ctx context.Context, client *remote.Client, database domainconfig.Database, emit func(domainruntime.Step, string, string, bool)) (string, string, error) {
	guardName := ".sqlturbo_lock_guard"
	guardPath := path.Join(database.WorkPath, guardName)

	for attempt := 0; ; attempt++ {
		emit(domainruntime.StepAcquireLock, "正在检查远程锁文件", "-", false)

		guardCreated, err := client.CreateExclusiveFile(guardPath, s.snapshot.LockContent())
		if err != nil {
			return "", "", err
		}
		if !guardCreated {
			if attempt >= s.app.RetryTimes {
				return "", "", fmt.Errorf("远程目录存在锁竞争，请稍后重试")
			}

			emit(domainruntime.StepWaitingLock, "等待锁协调文件释放["+guardName+"]", fmt.Sprintf("%ds", s.app.WaitTime), false)
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(time.Duration(s.app.WaitTime) * time.Second):
			}
			continue
		}

		entries, err := client.ListEntries(database.WorkPath)
		if err != nil {
			_ = client.RemoveFile(guardPath)
			return "", "", err
		}

		lockFiles := make([]string, 0)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "lock_") {
				lockFiles = append(lockFiles, entry.Name())
			}
		}

		if len(lockFiles) == 0 {
			lockName := "lock_" + time.Now().Format("20060102150405000")
			lockPath := path.Join(database.WorkPath, lockName)

			emit(domainruntime.StepCreateLock, "正在创建锁["+lockName+"]", "-", false)
			lockCreated, err := client.CreateExclusiveFile(lockPath, s.snapshot.LockContent())
			guardRemoveErr := client.RemoveFile(guardPath)
			if err != nil {
				return "", "", err
			}
			if guardRemoveErr != nil {
				return "", "", guardRemoveErr
			}
			if !lockCreated {
				if attempt >= s.app.RetryTimes {
					return "", "", fmt.Errorf("远程锁创建失败，请稍后重试")
				}

				emit(domainruntime.StepWaitingLock, "锁创建竞争，准备重试", fmt.Sprintf("%ds", s.app.WaitTime), false)
				select {
				case <-ctx.Done():
					return "", "", ctx.Err()
				case <-time.After(time.Duration(s.app.WaitTime) * time.Second):
				}
				continue
			}
			return lockName, lockPath, nil
		}

		_ = client.RemoveFile(guardPath)

		if attempt >= s.app.RetryTimes {
			return "", "", fmt.Errorf("远程目录存在未释放锁：%s", strings.Join(lockFiles, ", "))
		}

		emit(domainruntime.StepWaitingLock, "等待锁释放["+strings.Join(lockFiles, ", ")+"]", fmt.Sprintf("%ds", s.app.WaitTime), false)
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(time.Duration(s.app.WaitTime) * time.Second):
		}
	}
}

// removeRemoteSQLFiles 会删除远程工作目录中的历史 SQL 文件。
func (s *Service) removeRemoteSQLFiles(client *remote.Client, database domainconfig.Database, emit func(domainruntime.Step, string, string, bool)) error {
	emit(domainruntime.StepDeleteHistory, "正在删除远程历史SQL脚本", "-", false)

	entries, err := client.ListEntries(database.WorkPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".sql") {
			continue
		}
		if err := client.RemoveFile(path.Join(database.WorkPath, entry.Name())); err != nil {
			return err
		}
	}

	return nil
}

// uploadFiles 会上传一组文件，并在上传时实时回传百分比。
func (s *Service) uploadFiles(client *remote.Client, database domainconfig.Database, files []string, emit func(domainruntime.Step, string, string, bool)) error {
	for _, localPath := range files {
		fileName := filepath.Base(localPath)
		remotePath := path.Join(database.WorkPath, fileName)

		err := client.UploadFile(localPath, remotePath, func(written int64, total int64) {
			progress := formatPercent(written, total)
			emit(domainruntime.StepUploading, "正在上传["+fileName+"]", progress, false)
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) uploadProfileFiles(client *remote.Client, database domainconfig.Database, files []infraAssets.ProfileFile, emit func(domainruntime.Step, string, string, bool)) error {
	for _, profileFile := range files {
		remotePath := path.Join(database.WorkPath, profileFile.Name)
		content := normalizeScriptContent(profileFile.Content)

		err := client.UploadContent(remotePath, content, func(written int64, total int64) {
			progress := formatPercent(written, total)
			emit(domainruntime.StepUploading, "正在上传["+profileFile.Name+"]", progress, false)
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func normalizeScriptContent(content []byte) []byte {
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	content = bytes.ReplaceAll(content, []byte("\r"), []byte("\n"))
	return content
}

// runRemoteScript 会执行远程脚本，并按秒刷新执行耗时。
func (s *Service) runRemoteScript(ctx context.Context, client *remote.Client, scriptName string, command string, emit func(domainruntime.Step, string, string, bool)) error {
	errCh := make(chan error, 1)
	startAt := time.Now()
	currentSQLFile := ""
	var sqlFileMutex sync.RWMutex

	go func() {
		errCh <- client.RunCommandStream(ctx, command, func(line string) {
			if fileName := parseExecutingSQLFromLine(line); fileName != "" {
				sqlFileMutex.Lock()
				currentSQLFile = fileName
				sqlFileMutex.Unlock()
			}
		})
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case err := <-errCh:
			if err != nil {
				return err
			}
			emit(domainruntime.StepExecuting, "远程脚本执行完成", fmt.Sprintf("%ds", int(time.Since(startAt).Seconds())), false)
			return nil
		case <-ticker.C:
			sqlFileMutex.RLock()
			latestSQL := currentSQLFile
			sqlFileMutex.RUnlock()
			if latestSQL != "" {
				emit(domainruntime.StepExecuting, "正在执行["+latestSQL+"]", fmt.Sprintf("%ds", int(time.Since(startAt).Seconds())), false)
			} else {
				emit(domainruntime.StepExecuting, "正在执行["+scriptName+"]", fmt.Sprintf("%ds", int(time.Since(startAt).Seconds())), false)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func parseExecutingSQLFromLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if strings.HasPrefix(line, "正在执行:") {
		return strings.TrimSpace(strings.TrimPrefix(line, "正在执行:"))
	}
	if strings.HasPrefix(line, "正在执行：") {
		return strings.TrimSpace(strings.TrimPrefix(line, "正在执行："))
	}
	return ""
}

// downloadRemoteLog 会把远程 info.log 下载到本地 logs 目录。
func (s *Service) downloadRemoteLog(client *remote.Client, database domainconfig.Database, emit func(domainruntime.Step, string, string, bool)) error {
	localPath := filepath.Join(s.rootDir, "logs", database.ID+"_info.log")
	remotePath := path.Join(database.WorkPath, "info.log")

	return client.DownloadFile(remotePath, localPath, func(written int64, total int64) {
		progress := formatPercent(written, total)
		emit(domainruntime.StepDownloadingLog, "正在下载["+database.ID+"_info.log]", progress, false)
	})
}

// listLocalSQLFiles 会读取当前项目根目录下的所有 SQL 文件。
func (s *Service) listLocalSQLFiles() ([]string, error) {
	files, err := filepath.Glob(filepath.Join(s.rootDir, "*.sql"))
	if err != nil {
		return nil, fmt.Errorf("扫描本地SQL文件失败：%w", err)
	}
	sort.Strings(files)
	return files, nil
}

// buildExecutionCommand 会构建远程 shell 执行命令。
func buildExecutionCommand(database domainconfig.Database, scriptName string) string {
	quotedArgs := make([]string, 0, len(database.CommandArgs()))
	for _, arg := range database.CommandArgs() {
		quotedArgs = append(quotedArgs, shellQuote(arg))
	}

	parts := []string{fmt.Sprintf("cd %s", shellQuote(database.WorkPath))}
	if envPath := strings.TrimSpace(database.RuntimePathDirectory()); envPath != "" {
		parts = append(parts, envPath)
	}
	parts = append(parts, fmt.Sprintf("./%s %s", scriptName, strings.Join(quotedArgs, " ")))
	return strings.Join(parts, " && ")
}

// shellQuote 会把参数包装成安全的单引号形式，避免远程 shell 解析错误。
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// formatPercent 把字节进度格式化为可展示的百分比。
func formatPercent(written int64, total int64) string {
	if total <= 0 {
		return "0%"
	}
	percent := int(float64(written) / float64(total) * 100)
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("%d%%", percent)
}
