// Package e2e 提供本仓（根模块测试与嵌套模块测试）共用的 kitex e2e 测试基座：
// 真实 server/client 的启动 helper。internal 约束挡住外部引用；嵌套模块
// （kitexotel 等）import 根模块 internal 路径合法（module 路径前缀同源）。
package e2e

import (
	"net"
	"testing"
	"time"

	"github.com/cloudwego/kitex/server"

	echo "github.com/oekit/kitex/internal/e2e/kitex_gen/kitex/echo"
	echoservice "github.com/oekit/kitex/internal/e2e/kitex_gen/kitex/echo/echoservice"
)

// StartEchoServer 起一个真实 kitex echo server（随机端口），返回地址；
// 测试结束自动 Stop。
//
// 收编说明（#301）：原为根模块 bridge_e2e_test.go 与嵌套模块
// kitexotel/e2e_meta_test.go 各自持有的 42 行逐字同体 helper，收敛单源；
// 签名收 echo.EchoService 接口（原 kitexotel 版收具体 *echoHandler 指针）。
func StartEchoServer(t *testing.T, handler echo.EchoService, opts ...server.Option) string {
	t.Helper()
	// noctx 纪律：监听/拨号一律走 ListenConfig/Dialer 的 Context 形态；
	// 探活挂 t.Context()——测试结束自动中止探活循环，不悬空。
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	tcpAddr, taerr := net.ResolveTCPAddr("tcp", addr)
	if taerr != nil {
		t.Fatalf("resolve addr: %v", taerr)
	}
	base := make([]server.Option, 0, 2+len(opts))
	base = append(base,
		server.WithServiceAddr(tcpAddr),
		server.WithExitWaitTime(200*time.Millisecond), // 测试快速排空
	)
	svr := echoservice.NewServer(handler, append(base, opts...)...)
	runErr := make(chan error, 1)
	go func() {
		runErr <- svr.Run()
	}()
	go func() {
		if err := <-runErr; err != nil {
			t.Errorf("kitex server Run failed: %v", err)
		}
	}()
	t.Cleanup(func() { _ = svr.Stop() })

	deadline := time.Now().Add(3 * time.Second)
	dialer := net.Dialer{Timeout: 100 * time.Millisecond}
	for time.Now().Before(deadline) {
		c, derr := dialer.DialContext(t.Context(), "tcp", addr)
		if derr == nil {
			_ = c.Close()
			return addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("e2e server not ready in time")
	return ""
}
