package runtime

// Step 定义数据库执行过程中的阶段名称，用于 TUI 实时展示。
type Step string

const (
	StepPending        Step = "等待开始"
	StepInitializing   Step = "初始化"
	StepAcquireLock    Step = "获取锁"
	StepWaitingLock    Step = "等待锁释放"
	StepCreateLock     Step = "锁创建中"
	StepDeleteHistory  Step = "删除历史脚本"
	StepUploading      Step = "脚本上传中"
	StepVerifyingHash  Step = "哈希校验中"
	StepExecuting      Step = "脚本执行中"
	StepDownloadingLog Step = "日志下载中"
	StepReleasingLock  Step = "锁释放中"
	StepCompleted      Step = "完成"
	StepFailed         Step = "失败"
)

// StatusUpdate 是应用层推送给展示层的实时状态消息。
type StatusUpdate struct {
	DatabaseID string
	Step       Step
	Message    string
	Progress   string
	Failed     bool
}
