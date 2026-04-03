package runtime

// Step 定义数据库执行流程中的阶段名称，用于 TUI 实时展示。
type Step string

const (
	// StepPending 表示该数据库任务还未开始。
	StepPending Step = "等待开始"
	// StepInitializing 表示正在做前置准备，例如预计算哈希。
	StepInitializing Step = "初始化"
	// StepAcquireLock 表示正在尝试获取远程锁。
	StepAcquireLock Step = "获取锁"
	// StepWaitingLock 表示正在等待远程锁释放。
	StepWaitingLock Step = "等待锁释放"
	// StepCreateLock 表示正在创建本次执行锁。
	StepCreateLock Step = "锁创建中"
	// StepDeleteHistory 表示正在清理远程历史 SQL 文件。
	StepDeleteHistory Step = "删除历史脚本"
	// StepUploading 表示正在上传脚本或策略文件。
	StepUploading Step = "脚本上传中"
	// StepVerifyingHash 表示正在校验文件一致性。
	StepVerifyingHash Step = "哈希校验中"
	// StepExecuting 表示正在执行远程脚本。
	StepExecuting Step = "脚本执行中"
	// StepDownloadingLog 表示正在下载执行日志。
	StepDownloadingLog Step = "日志下载中"
	// StepReleasingLock 表示正在释放远程锁。
	StepReleasingLock Step = "锁释放中"
	// StepCompleted 表示该数据库任务已经完成。
	StepCompleted Step = "完成"
	// StepFailed 表示该数据库任务执行失败。
	StepFailed Step = "失败"
)

// StatusUpdate 表示应用层推送给展示层的实时状态。
type StatusUpdate struct {
	// DatabaseID 标识当前状态属于哪一个数据库。
	DatabaseID string
	// Step 表示当前所处的流程阶段。
	Step Step
	// Message 表示界面上展示的说明文本。
	Message string
	// Progress 表示界面上展示的进度文本。
	Progress string
	// Failed 表示当前阶段是否已经失败。
	Failed bool
}
