// The client's identity (InitConnection), as Telethon's TelegramClient made it with the defaults
// scripts/telegram-sync.py and plugins/telegram_live.py used: the converted session keeps the same
// auth key, and so it must keep looking like the same client in the account's list of sessions.
package telegram

import (
	"regexp"

	"github.com/gotd/td/telegram"
)

// telethonVersion is the app_version Telethon sent (its own version, 1.45.0 when the session was
// made).
const telethonVersion = "1.45.0"

var releaseTail = regexp.MustCompile(`-.+`)

// deviceModel is Telethon's default device_model from platform.uname().machine.
func deviceModel(machine string) string {
	switch machine {
	case "x86_64", "AMD64":
		return "PC 64bit"
	case "i386", "i686", "x86":
		return "PC 32bit"
	case "":
		return "Unknown"
	}
	return machine
}

// systemVersion is Telethon's default system_version from platform.uname().release.
func systemVersion(release string) string {
	if v := releaseTail.ReplaceAllString(release, ""); v != "" {
		return v
	}
	return "1.0"
}

// device is the identity sent on every connection: Telethon's defaults (lang "en", no lang pack,
// no params).
func device() telegram.DeviceConfig {
	machine, release := uname()
	return telegram.DeviceConfig{
		DeviceModel:    deviceModel(machine),
		SystemVersion:  systemVersion(release),
		AppVersion:     telethonVersion,
		LangCode:       "en",
		SystemLangCode: "en",
		LangPack:       "",
	}
}
