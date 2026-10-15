package catalog

import (
	"regexp"
	"strings"
)

var informationalParts = regexp.MustCompile(`(?i)(?:remaining traffic|traffic remaining|traffic left|remaining|осталось|остаток|剩余流量|剩余|流量剩余)\s*[:：]?\s*[0-9]+(?:[.,][0-9]+)?\s*(?:[kmgt]i?b|[кмгт]и?б|bytes?)|(?:expiry date|expires|expire|expiry|истекает|действует до|срок до|到期时间|到期)\s*[:：]?\s*[0-9]{4}[-/.][0-9]{1,2}[-/.][0-9]{1,2}(?:[ T][0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?)?`)

func stableInformationLabel(label string) (string, bool) {
	stripped := informationalParts.ReplaceAllString(label, "")
	return strings.Join(strings.Fields(strings.Trim(stripped, " \t|·—-:()[]")), " "), stripped != label
}

// Only recognized quota/expiry edits can bypass flash. Ordinary names, country,
// city, credentials, membership and transport changes still require a generation.
func InformationalChangesOnly(previous, next []Node) bool {
	if len(previous) != len(next) {
		return false
	}
	old := make(map[string]Node, len(previous))
	for _, n := range previous {
		old[n.ID] = n
	}
	changed := false
	for _, n := range next {
		before, ok := old[n.ID]
		if !ok || before.SelectionIdentity != n.SelectionIdentity || runtimeIdentity(before) != runtimeIdentity(n) || before.Country != n.Country || (before.City != n.City && (before.City != before.Label || n.City != n.Label)) {
			return false
		}
		delete(old, n.ID)
		if before.Label != n.Label {
			a, ai := stableInformationLabel(before.Label)
			b, bi := stableInformationLabel(n.Label)
			if !ai || !bi || a != b {
				return false
			}
			changed = true
		}
	}
	return changed
}
