package sip003

import "testing"

func TestPluginArchitecturePins(t *testing.T) {
	for _, arch := range []string{"aarch64_generic", "aarch64_cortex-a53", "x86_64"} {
		for _, name := range []string{"v2ray-plugin", "obfs-local"} {
			url, size, pin, err := pinnedPackage(arch, name)
			if err != nil || url != releaseURL+"packages_apk_"+arch+".zip" || size <= pin.Size || pin.File != pins[name].File || len(pin.SHA) != 64 {
				t.Fatalf("invalid %s/%s pin: %v", arch, name, err)
			}
			if arch == "x86_64" && pin.SHA == pins[name].SHA {
				t.Fatal("x86 uses an ARM package")
			}
		}
	}
	for _, arch := range []string{"", "amd64", "x86_64-extra", "mipsel_24kc"} {
		if _, _, _, err := pinnedPackage(arch, "v2ray-plugin"); err == nil {
			t.Fatalf("unsupported architecture %q accepted", arch)
		}
	}
	if _, _, _, err := pinnedPackage("x86_64", "unknown"); err == nil {
		t.Fatal("unknown plugin accepted")
	}
}
