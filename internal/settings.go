package internal

import (
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
