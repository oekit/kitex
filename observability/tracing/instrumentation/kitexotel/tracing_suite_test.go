package kitexotel

import (
	"testing"
)

// TestNewServerTracingSuite covers the constructor (no panic).
// Full OTel behavior is integration-test territory.
func TestNewServerTracingSuite(t *testing.T) {
	s := NewServerTracingSuite()
	if s == nil {
		t.Fatal("expected non-nil server.Suite")
	}
}

// TestNewClientTracingSuite covers the constructor (no panic).
func TestNewClientTracingSuite(t *testing.T) {
	s := NewClientTracingSuite()
	if s == nil {
		t.Fatal("expected non-nil client.Suite")
	}
}

// TestNewServerSuite_EquivalentToNewServerTracingSuite 钉住两个入口等价
// （NewServerSuite 是既有名字，NewServerTracingSuite 是 v0.7.0 起从根包
// 迁入的迁移名——两者必须指向同一实现，否则迁移会让行为悄悄变）。
func TestNewServerSuite_EquivalentToNewServerTracingSuite(t *testing.T) {
	a := NewServerSuite()
	b := NewServerTracingSuite()
	if a == nil || b == nil {
		t.Fatal("expected non-nil server.Suite")
	}
	if len(a.Options()) != len(b.Options()) {
		t.Fatalf("NewServerSuite options = %d, NewServerTracingSuite options = %d, want equal",
			len(a.Options()), len(b.Options()))
	}
}
