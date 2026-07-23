package main

import "strings"

// parseTags splits the -tags flag value into individual freeform "key=value"
// tags (same convention as plakman's sla_templates.tags), trimming whitespace
// around each entry and dropping empty ones (e.g. from a trailing or doubled
// comma). No format is enforced -- plakman doesn't validate tag shape either.
// An input with nothing usable (empty string, or only commas/whitespace)
// returns nil, matching PollRequest.Tags' omitempty wire behavior for "no
// tags configured".
func parseTags(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		tags = append(tags, p)
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}
