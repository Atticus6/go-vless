package main

import (
	"github.com/atticus6/go-vless/internal/config"
	"github.com/atticus6/go-vless/internal/server"
	"github.com/atticus6/go-vless/internal/sysutil"
)

func main() {
	cfg := config.Parse()

	// 启动前: 非容器环境自动检查并开启 BBR (尽力而为, 失败不阻塞启动)
	sysutil.EnsureBBR()

	server.Run(cfg)
}
