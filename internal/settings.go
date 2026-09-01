package internal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Muxcore-Media/core/pkg/contracts"
)

func (m *Module) Settings() []contracts.SettingDef {
	return m.settingsDefs()
}

func (m *Module) UpdateSetting(key, value string) error {
	return m.updateSetting(key, value)
}

func (m *Module) settingsDefs() []contracts.SettingDef {
	return []contracts.SettingDef{
		{
			Key:         "require_accessible",
			Label:       "Require Accessible Path",
			Type:        contracts.SettingTypeBool,
			Value:       strconv.FormatBool(m.getRequireAccessible()),
			Default:     "true",
			Description: "Reject Create/Update when the path is missing or unreadable (ROOTS_REQUIRE_ACCESSIBLE)",
			Group:       "Validation",
		},
	}
}

func (m *Module) updateSetting(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case "require_accessible", "ROOTS_REQUIRE_ACCESSIBLE":
		v, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid require_accessible %q (true/false)", value)
		}
		m.cfgMu.Lock()
		m.requireAccessible = v
		m.cfgMu.Unlock()
		if err := m.persistSetting(context.Background(), settingKeyRequireAccessible, value); err != nil {
			return fmt.Errorf("persist require_accessible: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
}

func (m *Module) getRequireAccessible() bool {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.requireAccessible
}

func (m *Module) loadPersistedSettings(ctx context.Context) error {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return nil
	}
	var value string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings_kv WHERE key = ?`, settingKeyRequireAccessible).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load require_accessible: %w", err)
	}
	v, parseErr := strconv.ParseBool(strings.TrimSpace(value))
	if parseErr != nil {
		return fmt.Errorf("parse persisted require_accessible: %w", parseErr)
	}
	m.cfgMu.Lock()
	m.requireAccessible = v
	m.cfgMu.Unlock()
	return nil
}

func (m *Module) persistSetting(ctx context.Context, key, value string) error {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return fmt.Errorf("database not open")
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO settings_kv (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}
