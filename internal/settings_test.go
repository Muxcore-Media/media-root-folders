package internal

import "testing"

func TestSettingsRequireAccessible(t *testing.T) {
	m := NewModule(Config{})
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
