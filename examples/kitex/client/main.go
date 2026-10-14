package main

import (
	"context"
	"os"

	ekitkitex "github.com/yi-nology/ekit-kitex/kitex"
	registrykitex "github.com/yi-nology/ekit-kitex/registry/kitex"
	"github.com/yi-nology/ekit-logx"
	"github.com/yi-nology/ekit-tracing"
	"github.com/yi-nology/ekit-registry/config"
	"github.com/yi-nology/ekit-registry/nacos"
	nacosimpl "github.com/yi-nology/ekit-registry/registry/nacos"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/klog"
	echo "github.com/yi-nology/ekit-kitex/examples/kitex/kitex_gen/kitex/echo"
	"github.com/yi-nology/ekit-kitex/examples/kitex/kitex_gen/kitex/echo/echoservice"
)

type AppConfig struct {
	ServiceName  string          `yaml:"service_name"`
	TargetServer string          `yaml:"target_server"`
	Nacos        nacos.Bootstrap `yaml:"nacos"`
	Tracing      tracing.Config  `yaml:"tracing"`
}

func main() {
	cfg, err := config.LoadBootstrap[AppConfig]("dev")
	if err != nil {
		logx.Error("load config failed", "err", err)
		os.Exit(1)
	}

	// 1. 日志桥接（必须传 slog 实例避免无限递归）
	klog.SetLogger(ekitkitex.NewLogger(logx.NewSlogLogger(cfg.ServiceName)))

	// 2. 创建 Nacos naming client → ekit Discoverer → Kitex Resolver
	namingCli, err := nacos.NewNamingClient(cfg.Nacos)
	if err != nil {
		logx.Error("create naming client failed", "err", err)
		os.Exit(1)
	}
	disc, err := nacosimpl.NewDiscoverer(namingCli)
	if err != nil {
		logx.Error("create discoverer failed", "err", err)
		os.Exit(1)
	}

	// 3. 创建 Kitex client（带服务发现 + 可观测性）
	cli, err := echoservice.NewClient(cfg.TargetServer,
		client.WithResolver(registrykitex.NewResolver(disc)),
		client.WithSuite(ekitkitex.NewClientTracingSuite()),
	)
	if err != nil {
		logx.Error("create client failed", "err", err)
		os.Exit(1)
	}

	// 4. 调用
	resp, err := cli.Echo(context.Background(), &echo.EchoRequest{
		Message: "hello from ekit",
	})
	if err != nil {
		logx.Error("echo call failed", "err", err)
		os.Exit(1)
	}

	logx.Info("response received", "message", resp.Message, "timestamp", resp.Timestamp)
}
