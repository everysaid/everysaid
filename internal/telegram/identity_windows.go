//go:build windows

package telegram

import (
	"runtime"

	"golang.org/x/sys/windows"
)

// uname is Python's platform.uname() machine and release on Windows: "AMD64", "ARM64" or "x86";
// "10", or "11" from build 22000 on.
func uname() (string, string) {
	machine := map[string]string{"amd64": "AMD64", "arm64": "ARM64", "386": "x86"}[runtime.GOARCH]
	v := windows.RtlGetVersion()
	release := ""
	switch {
	case v.MajorVersion == 10 && v.BuildNumber >= 22000:
		release = "11"
	case v.MajorVersion == 10:
		release = "10"
	case v.MajorVersion == 6 && v.MinorVersion == 3:
		release = "8.1"
	case v.MajorVersion == 6 && v.MinorVersion == 2:
		release = "8"
	case v.MajorVersion == 6 && v.MinorVersion == 1:
		release = "7"
	}
	return machine, release
}
