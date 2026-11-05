package xray

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/xtls/xray-core/core"
)

func TestCompiledXrayMatchesPinnedSource(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("missing source location")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../third_party/xray-core/core/core.go"))
	if os.IsNotExist(err) {
		t.Skip("standalone test binary has no pinned source tree")
	}
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`Version_[xyz]\s+byte\s*=\s*(\d+)`).FindAllSubmatch(body, -1)
	if len(matches) != 3 {
		t.Fatal("missing pinned core version")
	}
	parts := make([]string, 3)
	for i, match := range matches {
		parts[i] = string(match[1])
	}
	if want := strings.Join(parts, "."); core.Version() != want {
		t.Fatalf("compiled Xray %s, pinned source %s; refresh vendor", core.Version(), want)
	}
}
