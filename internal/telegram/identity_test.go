package telegram

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

func TestIdentityRules(t *testing.T) {
	for in, want := range map[string]string{"x86_64": "PC 64bit", "AMD64": "PC 64bit", "i686": "PC 32bit", "arm64": "arm64", "": "Unknown"} {
		if got := deviceModel(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	for in, want := range map[string]string{"6.8.0-1-generic": "6.8.0", "23.1.0": "23.1.0", "": "1.0", "6.1-": "6.1-", "-x": "1.0"} {
		if got := systemVersion(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	d := device()
	if d.AppVersion != telethonVersion || d.LangCode != "en" || d.SystemLangCode != "en" || d.LangPack != "" || d.Params != nil {
		t.Fatalf("%+v", d)
	}
}

// TestIdentityAsTelethon asks Telethon itself (the project's .venv, where there is one) what its
// InitConnection would carry on this machine; nothing connects.
func TestIdentityAsTelethon(t *testing.T) {
	py := "../../.venv/bin/python"
	if _, err := os.Stat(py); err != nil {
		t.Skip("no .venv with Telethon")
	}
	out, err := exec.Command(py, "-c", `
import json, telethon
from telethon import TelegramClient
from telethon.sessions import StringSession
r = TelegramClient(StringSession(), 1, "x")._init_request
print(json.dumps({"device_model": r.device_model, "system_version": r.system_version, "app_version": r.app_version,
  "lang_code": r.lang_code, "system_lang_code": r.system_lang_code, "lang_pack": r.lang_pack, "version": telethon.__version__}))
`).Output()
	if err != nil {
		t.Skip("Telethon not importable:", err)
	}
	var want map[string]string
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	d := device()
	got := map[string]string{"device_model": d.DeviceModel, "system_version": d.SystemVersion, "app_version": d.AppVersion,
		"lang_code": d.LangCode, "system_lang_code": d.SystemLangCode, "lang_pack": d.LangPack, "version": telethonVersion}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, Telethon %q", k, got[k], v)
		}
	}
}
