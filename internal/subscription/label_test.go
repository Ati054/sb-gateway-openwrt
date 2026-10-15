package subscription

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSubscriptionLabelIsBoundedDisplayText(t *testing.T) {
	html := `<img src=x onerror=alert(document.cookie)>`
	nodes, err := Parse("fixture", []byte("vless://00000000-0000-0000-0000-000000000001@example.com:443#"+url.PathEscape(html)), 10)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("parse: %v, nodes=%d", err, len(nodes))
	}
	if nodes[0].Label != html {
		t.Fatal("HTML must remain literal display text")
	}
	if got := cleanLabel(" 🇫🇷 A\x00\nB\u202eC 👩‍💻 "); got != "🇫🇷 ABC 👩‍💻" {
		t.Fatalf("controls/emoji: %q", got)
	}
	long := cleanLabel(strings.Repeat("😀", 300))
	if !utf8.ValidString(long) || utf8.RuneCountInString(long) != 256 || len(long) > 1024 {
		t.Fatal("label bound is not Unicode safe")
	}
}
