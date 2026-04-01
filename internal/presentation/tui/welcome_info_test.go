package tui

import (
	"strings"
	"testing"

	"sqlturbo/internal/version"
)

func TestBuildWelcomeBannerUsesVersionPackageValue(t *testing.T) {
	originalVersion := version.Version
	t.Cleanup(func() {
		version.Version = originalVersion
	})

	version.Version = "v9.9.9-test"
	banner := buildWelcomeBanner()

	expected := "版本号：" + version.Version
	if !strings.Contains(banner, expected) {
		t.Fatalf("欢迎页未使用 version.Version，期望包含: %q，实际内容: %q", expected, banner)
	}
}
