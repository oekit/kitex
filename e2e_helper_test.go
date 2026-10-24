package kitex

import (
	"context"

	echo "github.com/oekit/kitex/internal/e2e/kitex_gen/kitex/echo"
)

// echoHandler 是就绪门/启动辅助测试用最小 handler：原样回显 message。
//
// ⚠️ v0.7.0 起 e2e_meta_test.go 随嵌套模块 kitexotel 迁出根仓（根包测试
// 不得引 kitexotel，否则根模块依赖面又会被拖回 apache/thrift v0.16.0，#37）——
// 本文件保留根仓自己需要的最小 handler。
type echoHandler struct{}

func (h *echoHandler) Echo(_ context.Context, req *echo.EchoRequest) (*echo.EchoResponse, error) {
	return &echo.EchoResponse{Message: req.Message, Timestamp: 1}, nil
}
