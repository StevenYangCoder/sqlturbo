package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Manager 管理应用运行时的日志文件和共享 logger。
type Manager struct {
	// logger 是对外提供的统一日志实例。
	logger *slog.Logger
	// infoFile 接收 info 级别以下的日志。
	infoFile *os.File
	// errorFile 接收 error 级别及以上的日志。
	errorFile *os.File
}

// NewManager 创建日志目录、打开日志文件并构造 logger。
func NewManager(rootDir string) (*Manager, error) {
	// 所有日志都写入根目录下的 logs 目录。
	logDir := filepath.Join(rootDir, "logs")
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

	// 低级别日志写入 info 文件。
	infoHandler := newLevelFilterHandler(slog.NewTextHandler(infoFile, options), func(level slog.Level) bool {
		return level < slog.LevelError
	})
	// error 及以上日志写入 error 文件。
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

// Logger 返回共享 logger。
func (m *Manager) Logger() *slog.Logger {
	return m.logger
}

// Close 关闭所有日志文件句柄。
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

// levelFilterHandler 根据日志级别决定是否转发到下游 handler。
type levelFilterHandler struct {
	next      slog.Handler
	predicate func(level slog.Level) bool
}

// newLevelFilterHandler 创建带级别过滤的 handler。
func newLevelFilterHandler(next slog.Handler, predicate func(level slog.Level) bool) slog.Handler {
	return &levelFilterHandler{
		next:      next,
		predicate: predicate,
	}
}

// Enabled 快速判断当前级别是否需要处理。
func (h *levelFilterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.predicate(level) && h.next.Enabled(ctx, level)
}

// Handle 将符合条件的日志转发给下游 handler。
func (h *levelFilterHandler) Handle(ctx context.Context, record slog.Record) error {
	if !h.predicate(record.Level) {
		return nil
	}
	return h.next.Handle(ctx, record)
}

// WithAttrs 在复制 handler 时保留过滤逻辑。
func (h *levelFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelFilterHandler{
		next:      h.next.WithAttrs(attrs),
		predicate: h.predicate,
	}
}

// WithGroup 在复制 handler 时保留过滤逻辑。
func (h *levelFilterHandler) WithGroup(name string) slog.Handler {
	return &levelFilterHandler{
		next:      h.next.WithGroup(name),
		predicate: h.predicate,
	}
}

// multiHandler 将一条日志分发到多个下游 handler。
type multiHandler struct {
	handlers []slog.Handler
}

// newMultiHandler 创建多路分发 handler。
func newMultiHandler(handlers ...slog.Handler) slog.Handler {
	return &multiHandler{handlers: handlers}
}

// Enabled 只要任意下游 handler 需要该日志，就返回 true。
func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle 把同一条日志广播给所有可处理的下游 handler。
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

// WithAttrs 为所有下游 handler 复制属性。
func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		handlers = append(handlers, handler.WithAttrs(attrs))
	}
	return &multiHandler{handlers: handlers}
}

// WithGroup 为所有下游 handler 复制分组。
func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		handlers = append(handlers, handler.WithGroup(name))
	}
	return &multiHandler{handlers: handlers}
}
