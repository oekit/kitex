package kitexotel

import (
	"testing"

	"go.opentelemetry.io/otel/propagation"
)

// TestNewServerSuite_ReturnsNonNil 验证 NewServerSuite 返回非 nil。
func TestNewServerSuite_ReturnsNonNil(t *testing.T) {
	suite := NewServerSuite()
	if suite == nil {
		t.Fatal("NewServerSuite returned nil")
	}
}

// TestNewServerSuite_WithOptions 验证带 option 构造。
func TestNewServerSuite_WithOptions(t *testing.T) {
	prop := propagation.NewCompositeTextMapPropagator()
	suite := NewServerSuite(
		WithTextMapPropagator(prop),
		WithRecordSourceOperation(true),
		WithEnableGRPCMetadata(),
	)
	if suite == nil {
		t.Fatal("NewServerSuite with opts returned nil")
	}
}

// TestNewTracingServerOptions_ReturnsList 验证返回 []server.Option 非空。
func TestNewTracingServerOptions_ReturnsList(t *testing.T) {
	opts := NewTracingServerOptions()
	if len(opts) == 0 {
		t.Fatal("NewTracingServerOptions returned empty list")
	}
}

// TestServerOption_TypeAlias 验证 type alias 编译期兼容。
func TestServerOption_TypeAlias(t *testing.T) {
	var _ = WithTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	var _ = WithRecordSourceOperation(true)
	var _ = WithEnableGRPCMetadata()
}
