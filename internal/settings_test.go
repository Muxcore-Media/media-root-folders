package internal

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSettingsRequireAccessible(t *testing.T) {
	m := newTestModule(t)
	if !m.getRequireAccessible() {
		t.Fatal("default should be true")
	}
	if err := m.UpdateSetting("require_accessible", "false"); err != nil {
		t.Fatal(err)
	}
	if m.getRequireAccessible() {
		t.Fatal("expected false")
	}
	if err := m.UpdateSetting("ROOTS_REQUIRE_ACCESSIBLE", "true"); err != nil {
		t.Fatal(err)
	}
	if !m.getRequireAccessible() {
		t.Fatal("expected true")
	}
	if err := m.UpdateSetting("unknown", "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSettingsRequireAccessiblePersisted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "roots.db")
	ctx := context.Background()

	m1 := NewModule(Config{DBPath: dbPath, GRPCAddr: ":0"})
	if err := m1.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m1.UpdateSetting("require_accessible", "false"); err != nil {
		t.Fatal(err)
	}
	if err := m1.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	m2 := NewModule(Config{DBPath: dbPath, GRPCAddr: ":0"})
	if err := m2.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m2.Stop(ctx) })

	if m2.getRequireAccessible() {
		t.Fatal("expected persisted false after restart")
	}
}
