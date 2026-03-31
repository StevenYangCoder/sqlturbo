package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Manager 负责管理应用日志文件与 slog 实例。
type Manager struct {
	logger    *slog.Logger
	infoFile  *os.File
	errorFile *os.File
}

// NewManager 会初始化 info/error 两类日志文件。
func NewManager(rootDir string) (*Manager, error) {
	logDir := filepath.Join(rootDir, "data", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败：%w", err)
	}

	infoFile, err := os.OpenFile(filepath.Join(logDir, "app_info.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("创建info日志失败：%w", err)
	}

	errorFile, err := os.OpenFile(filepath.Join(logDir, "app_error.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		infoFile.Close()
		return nil, fmt.Errorf("创建error日志失败：%w", err)
	}

	options := &slog.HandlerOptions{
		Level:     slog.LevelDebug,
		AddSource: false,
	}

	infoHandler := newLevelFilterHandler(slog.NewTextHandler(infoFile, options), func(level slog.Level) bool {
		return level < slog.LevelError
	})
	errorHandler := newLevelFilterHandler(slog.NewTextHandler(errorFile, options), func(level slog.Level) bool {
		return level >= slog.LevelError
	})

	logger := slog.New(newMultiHandler(infoHandler, errorHandler))

	return &Manager{
		logger:    logger,
		infoFile:  infoFile,
		errorFile: errorFile,
	}, nil
}

// Logger 返回共享的 slog 实例。
func (m *Manager) Logger() *slog.Logger {
	return m.logger
}

// Close 会关闭日志文件句柄。
func (m *Manager) Close() error {
	var closeErr error

	if m.infoFile != nil {
		if err := m.infoFile.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	if m.errorFile != nil {
		if err := m.errorFile.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}

	return closeErr
}

// levelFilterHandler 用于根据日志级别筛选输出目标。
type levelFilterHandler struct {
	next      slog.Handler
	predicate func(level slog.Level) bool
}

// newLevelFilterHandler 创建带级别筛选的 handler。
func newLevelFilterHandler(next slog.Handler, predicate func(level slog.Level) bool) slog.Handler {
	return &levelFilterHandler{
		next:      next,
		predicate: predicate,
	}
}

// Enabled 会在日志入口快速判断当前级别是否需要处理。
func (h *levelFilterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.predicate(level) && h.next.Enabled(ctx, level)
}

// Handle 负责真正转发符合条件的日志记录。
func (h *levelFilterHandler) Handle(ctx context.Context, record slog.Record) error {
	if !h.predicate(record.Level) {
		return nil
	}
	return h.next.Handle(ctx, record)
}

// WithAttrs 复制 handler 并附带属性。
func (h *levelFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelFilterHandler{
		next:      h.next.WithAttrs(attrs),
		predicate: h.predicate,
	}
}

// WithGroup 复制 handler 并进入属性分组。
func (h *levelFilterHandler) WithGroup(name string) slog.Handler {
	return &levelFilterHandler{
		next:      h.next.WithGroup(name),
		predicate: h.predicate,
	}
}

// multiHandler 会把一条日志分发到多个下游 handler。
type multiHandler struct {
	handlers []slog.Handler
}

// newMultiHandler 创建简单的 handler 组合器。
func newMultiHandler(handlers ...slog.Handler) slog.Handler {
	return &multiHandler{handlers: handlers}
}

// Enabled 只要任意一个子 handler 愿意处理就返回 true。
func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle 会把日志顺序分发给所有子 handler。
func (h *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			if err := handler.Handle(ctx, record.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

// WithAttrs 为所有子 handler 增加公共属性。
func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		handlers = append(handlers, handler.WithAttrs(attrs))
	}
	return &multiHandler{handlers: handlers}
}

// WithGroup 为所有子 handler 增加公共分组。
func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		handlers = append(handlers, handler.WithGroup(name))
	}
	return &multiHandler{handlers: handlers}
}
