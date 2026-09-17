// Package buildinfo reports the running version.
package buildinfo

import "runtime/debug"

// Version is set at link time with -ldflags "-X .../buildinfo.Version=v1.2.3".
var Version = "dev"

// String returns the version, falling back to module build metadata.
func String() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}

// UserAgent identifies devmodels to the sources it fetches.
func UserAgent() string {
	return "devmodels/" + String() + " (+https://github.com/Tim-Butterfield/devin-model-catalog)"
}
