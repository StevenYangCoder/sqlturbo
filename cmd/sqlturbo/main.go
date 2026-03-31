package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"sqlturbo/internal/application/bootstrap"
)

// main 是 SqlTurbo 的程序入口，负责构建可取消上下文并启动完整流程。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := bootstrap.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "程序运行失败：%v\n", err)
		os.Exit(1)
	}
}
