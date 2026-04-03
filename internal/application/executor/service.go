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
	// rootDir 是当前项目根目录，用于查找本地脚本和日志目录。
	rootDir string
	// app 保存应用级配置，例如并发数和重试次数。
	app domainconfig.Application
	// snapshot 保存执行锁内容等运行期快照。
	snapshot history.Snapshot
	// logger 用于输出运行日志。
	logger *slog.Logger
}

// uploadArtifact 表示一个待上传、待校验的文件单元。
type uploadArtifact struct {
	// name 是界面和日志里展示的文件名。
	name string
	// remotePath 是该文件在远端的完整路径。
	remotePath string
	// localPath 是磁盘上的本地路径，仅对本地文件使用。
	localPath string
	// content 是内存中的文件内容，仅对内置 profile 文件使用。
	content []byte
	// fromMemory 标记这个文件是否来自内存。
	fromMemory bool
	// localXXH3 是上传时同步计算出来的本地 xxHash3 值。
	localXXH3 string
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

// Run 并发执行所有已选数据库，并把实时状态持续推送到展示层。
func (s *Service) Run(ctx context.Context, databases []domainconfig.Database, notify func(domainruntime.StatusUpdate)) error {
	if len(databases) == 0 {
		return nil
	}

	// waitGroup 等待所有数据库任务完成。
	var waitGroup sync.WaitGroup
	// mutex 保护失败列表的并发写入。
	var mutex sync.Mutex
	// failures 收集每个失败任务的错误信息。
	failures := make([]string, 0)

	// concurrency 用来限制同时执行的数据库数量。
	concurrency := s.app.Concurrency
	if concurrency <= 0 || concurrency > len(databases) {
		concurrency = len(databases)
	}
	// limiter 是一个带缓冲通道，用作并发令牌。
	limiter := make(chan struct{}, concurrency)

	for _, database := range databases {
		// 先占用一个并发令牌，再启动 goroutine。
		limiter <- struct{}{}
		waitGroup.Add(1)

		// 每个数据库单独跑一个执行流程。
		go func(database domainconfig.Database) {
			defer waitGroup.Done()
			defer func() { <-limiter }()
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
	// logger 绑定数据库 ID，方便单库排查问题。
	logger := s.logger.With("数据库ID", database.ID)
	// emit 统一包装状态推送，避免各处重复拼装 DatabaseID。
	emit := func(step domainruntime.Step, message string, progress string, failed bool) {
		notify(domainruntime.StatusUpdate{
			DatabaseID: database.ID,
			Step:       step,
			Message:    message,
			Progress:   progress,
			Failed:     failed,
		})
	}

	// 先收集本地 SQL 文件。
	localSQLFiles, err := s.listLocalSQLFiles()
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}
	if len(localSQLFiles) == 0 {
		emit(domainruntime.StepCompleted, "当前目录下不存在SQL脚本，无须执行", "-", false)
		return nil
	}

	// 先读取 profile 文件，后续会一起上传。
	profileFiles, err := infraAssets.ListProfileFiles(database.ProfileDirectory())
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	// 这里只组装待上传清单，不提前计算本地哈希。
	artifacts, err := s.buildUploadArtifacts(database, localSQLFiles, profileFiles)
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	// 建立 SSH/SFTP 连接。
	emit(domainruntime.StepInitializing, "正在建立远程连接", "-", false)
	client, err := remote.NewClient(database)
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}
	defer client.Close()

	// 确保远端工作目录存在。
	if err := client.EnsureDir(database.WorkPath); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	// 获取远端目录锁，避免多个实例并发写同一个工作目录。
	lockName, lockPath, err := s.acquireLock(ctx, client, database, emit)
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	// releaseLock 负责在流程结束时清理业务锁。
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

	// 删除远端历史 SQL 文件，避免旧脚本干扰本次执行。
	if err := s.removeRemoteSQLFiles(client, database, emit); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	// 上传所有文件，并在上传时同步计算本地 xxHash3。
	if err := s.uploadArtifacts(client, artifacts, emit); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	// 统一计算远端文件哈希并与本地上传时产生的哈希对比。
	if err := s.verifyArtifactsXXH3(client, artifacts, emit); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	// 读取该数据库类型对应的执行脚本名。
	scriptName, err := database.ShellScriptName()
	if err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	// 给脚本加执行权限。
	chmodCommand := fmt.Sprintf("cd %s && chmod 555 %s", shellQuote(database.WorkPath), shellQuote(scriptName))
	if err := client.RunCommand(ctx, chmodCommand); err != nil {
		emit(domainruntime.StepFailed, err.Error(), "-", true)
		return err
	}

	// 组装最终执行命令。
	command := buildExecutionCommand(database, scriptName)
	logger.Info("执行远程脚本命令", "命令", command)

	// 执行远程脚本并下载日志。
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

// acquireLock 通过“协调锁 + 业务锁”两阶段协议实现远程目录锁。
func (s *Service) acquireLock(ctx context.Context, client *remote.Client, database domainconfig.Database, emit func(domainruntime.Step, string, string, bool)) (string, string, error) {
	// guardName 是协调阶段使用的临时文件名。
	guardName := ".sqlturbo_lock_guard"
	// guardPath 是协调锁文件的完整路径。
	guardPath := path.Join(database.WorkPath, guardName)
	// waitStartedAt 记录当前等待阶段的起始时间。
	waitStartedAt := time.Time{}

	// waitAndEmit 在等待锁释放时持续刷新进度。
	waitAndEmit := func(message string) error {
		if waitStartedAt.IsZero() {
			waitStartedAt = time.Now()
		}
		return waitWithProgress(ctx, s.app.WaitTime, waitStartedAt, func(progress string) {
			emit(domainruntime.StepWaitingLock, message, progress, false)
		})
	}

	// 尝试抢占协调锁，直到成功或超出重试次数。
	for attempt := 0; ; attempt++ {
		emit(domainruntime.StepAcquireLock, "正在检查远程锁文件", "-", false)

		// 先创建协调锁文件，避免多个实例同时进入下一步。
		guardCreated, err := client.CreateExclusiveFile(guardPath, s.snapshot.LockContent())
		if err != nil {
			return "", "", err
		}
		if !guardCreated {
			if attempt >= s.app.RetryTimes {
				return "", "", fmt.Errorf("远程目录存在锁竞争，请稍后重试")
			}
			if err := waitAndEmit("等待锁协调文件释放[" + guardName + "]"); err != nil {
				return "", "", err
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

		// 如果目录里没有业务锁，说明当前可以创建新的执行锁。
		if len(lockFiles) == 0 {
			// 生成一个唯一的业务锁名。
			lockName := "lock_" + time.Now().Format("20060102150405000")
			// 业务锁的完整路径。
			lockPath := path.Join(database.WorkPath, lockName)

			emit(domainruntime.StepCreateLock, "正在创建锁["+lockName+"]", "-", false)
			// 创建业务锁。
			lockCreated, err := client.CreateExclusiveFile(lockPath, s.snapshot.LockContent())
			// 无论创建成功与否，都先移除协调锁。
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
				if err := waitAndEmit("锁创建竞争，准备重试"); err != nil {
					return "", "", err
				}
				continue
			}
			return lockName, lockPath, nil
		}

		// 仍然存在业务锁，说明当前需要等待。
		_ = client.RemoveFile(guardPath)

		if attempt >= s.app.RetryTimes {
			return "", "", fmt.Errorf("远程目录存在未释放锁：%s", strings.Join(lockFiles, ", "))
		}
		if err := waitAndEmit("等待锁释放[" + strings.Join(lockFiles, ", ") + "]"); err != nil {
			return "", "", err
		}
	}
}

func waitWithProgress(ctx context.Context, waitSeconds int, startedAt time.Time, onTick func(progress string)) error {
	if waitSeconds <= 0 {
		return nil
	}

	onTick(formatElapsedSeconds(startedAt))

	waitDuration := time.Duration(waitSeconds) * time.Second
	timer := time.NewTimer(waitDuration)
	ticker := time.NewTicker(time.Second)
	defer timer.Stop()
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			onTick(formatElapsedSeconds(startedAt))
		case <-timer.C:
			onTick(formatElapsedSeconds(startedAt))
			return nil
		}
	}
}

func formatElapsedSeconds(startedAt time.Time) string {
	return fmt.Sprintf("%ds", int(time.Since(startedAt).Seconds()))
}

// removeRemoteSQLFiles 删除远程工作目录下的历史 SQL 文件。
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

// buildUploadArtifacts 只组装待上传文件列表，不在这里预先计算哈希。
func (s *Service) buildUploadArtifacts(database domainconfig.Database, localSQLFiles []string, profileFiles []infraAssets.ProfileFile) ([]uploadArtifact, error) {
	total := len(localSQLFiles) + len(profileFiles)
	if total == 0 {
		return nil, nil
	}

	artifacts := make([]uploadArtifact, 0, total)

	for _, localPath := range localSQLFiles {
		fileName := filepath.Base(localPath)
		artifacts = append(artifacts, uploadArtifact{
			name:       fileName,
			remotePath: path.Join(database.WorkPath, fileName),
			localPath:  localPath,
			fromMemory: false,
		})
	}

	for _, profileFile := range profileFiles {
		artifacts = append(artifacts, uploadArtifact{
			name:       profileFile.Name,
			remotePath: path.Join(database.WorkPath, profileFile.Name),
			content:    normalizeScriptContent(profileFile.Content),
			fromMemory: true,
		})
	}

	return artifacts, nil
}

// uploadArtifacts 按顺序上传所有待处理文件，并把上传时计算出的本地哈希写回 artifact。
func (s *Service) uploadArtifacts(client *remote.Client, artifacts []uploadArtifact, emit func(domainruntime.Step, string, string, bool)) error {
	for index := range artifacts {
		var (
			hashResult remote.UploadResult
			err        error
		)

		if artifacts[index].fromMemory {
			hashResult, err = client.UploadContentWithHash(artifacts[index].remotePath, artifacts[index].content, func(written int64, total int64) {
				emit(domainruntime.StepUploading, "正在上传并计算哈希["+artifacts[index].name+"]", formatPercent(written, total), false)
			})
		} else {
			hashResult, err = client.UploadFileWithHash(artifacts[index].localPath, artifacts[index].remotePath, func(written int64, total int64) {
				emit(domainruntime.StepUploading, "正在上传并计算哈希["+artifacts[index].name+"]", formatPercent(written, total), false)
			})
		}
		if err != nil {
			return err
		}

		artifacts[index].localXXH3 = hashResult.LocalXXH3
	}
	return nil
}

// verifyArtifactsXXH3 统一读取远端文件并对比本地上传时产生的 xxHash3。
func (s *Service) verifyArtifactsXXH3(client *remote.Client, artifacts []uploadArtifact, emit func(domainruntime.Step, string, string, bool)) error {
	for _, artifact := range artifacts {
		remoteXXH3, err := client.ComputeRemoteXXH3(artifact.remotePath, func(written int64, total int64) {
			emit(domainruntime.StepVerifyingHash, "正在校验哈希一致性["+artifact.name+"]", formatPercent(written, total), false)
		})
		if err != nil {
			return err
		}
		if remoteXXH3 != artifact.localXXH3 {
			_ = client.RemoveFile(artifact.remotePath)
			return fmt.Errorf("文件[%s]xxHash3不匹配：local=%s,remote=%s", artifact.name, artifact.localXXH3, remoteXXH3)
		}
	}
	return nil
}

// normalizeScriptContent 统一 profile 文件的编码和换行格式。
func normalizeScriptContent(content []byte) []byte {
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})
	content = bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	content = bytes.ReplaceAll(content, []byte("\r"), []byte("\n"))
	return content
}

// runRemoteScript 执行远程脚本并按秒刷新执行耗时。
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

// downloadRemoteLog 把远程 info.log 下载到本地 logs 目录。
func (s *Service) downloadRemoteLog(client *remote.Client, database domainconfig.Database, emit func(domainruntime.Step, string, string, bool)) error {
	localPath := filepath.Join(s.rootDir, "logs", database.ID+"_info.log")
	remotePath := path.Join(database.WorkPath, "info.log")

	return client.DownloadFile(remotePath, localPath, func(written int64, total int64) {
		emit(domainruntime.StepDownloadingLog, "正在下载["+database.ID+"_info.log]", formatPercent(written, total), false)
	})
}

// listLocalSQLFiles 读取项目根目录中的全部 SQL 文件。
func (s *Service) listLocalSQLFiles() ([]string, error) {
	files, err := filepath.Glob(filepath.Join(s.rootDir, "*.sql"))
	if err != nil {
		return nil, fmt.Errorf("扫描本地SQL文件失败：%w", err)
	}
	sort.Strings(files)
	return files, nil
}

// buildExecutionCommand 构建远程 shell 执行命令。
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

// shellQuote 把参数包装成安全的单引号形式，避免远程 shell 解析错误。
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
