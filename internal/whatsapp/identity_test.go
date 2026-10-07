package whatsapp

// The device must stay the one the standalone bridge linked: one key, one identity. The bridge sets
// nothing of its own (no store.SetOSInfo, DeviceProps or BaseClientPayload change), so what WhatsApp
// sees is whatsmeow's defaults at the bridge's version. A newer whatsmeow, or any code of the binary
// changing those globals, fails here.

import (
	"runtime/debug"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/store"
)

// The whatsmeow of bridges/whatsapp/go.mod.
const bridgeWhatsmeow = "v0.0.0-20260929112325-8b41cfe6d9c4"

func TestTheDeviceIsTheBridges(t *testing.T) {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, d := range info.Deps {
			if d.Path == "go.mau.fi/whatsmeow" && d.Version != bridgeWhatsmeow {
				t.Errorf("whatsmeow %s, the bridge's is %s", d.Version, bridgeWhatsmeow)
			}
		}
	}
	p := store.DeviceProps
	if p.GetOs() != "whatsmeow" || p.GetVersion().GetPrimary() != 0 || p.GetVersion().GetSecondary() != 1 ||
		p.GetVersion().GetTertiary() != 0 || p.GetPlatformType() != waCompanionReg.DeviceProps_UNKNOWN {
		t.Errorf("device props %v", p)
	}
	u := store.BaseClientPayload.GetUserAgent()
	v := u.GetAppVersion()
	if u.GetPlatform() != waWa6.ClientPayload_UserAgent_WEB || u.GetDevice() != "Desktop" || u.GetOsVersion() != "0.1" ||
		v.GetPrimary() != 2 || v.GetSecondary() != 3000 || v.GetTertiary() != 1048620361 ||
		store.BaseClientPayload.GetWebInfo().GetWebSubPlatform() != waWa6.ClientPayload_WebInfo_WEB_BROWSER {
		t.Errorf("client payload %v", u)
	}
}
