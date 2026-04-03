package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"sqlturbo/internal/application/bootstrap"
)

// main 是程序入口，负责创建可取消的上下文并启动完整执行流程。
func main() {
	// 监听系统中断信号，让程序可以优雅退出。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 交给 bootstrap 层执行完整应用流程。
	if err := bootstrap.Run(ctx); err != nil {
		// 输出错误到标准错误流，并以非零状态退出。
		fmt.Fprintf(os.Stderr, "程序运行失败：%v\n", err)
		os.Exit(1)
	}
}
