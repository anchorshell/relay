package characterization

import "unicode/utf8"

// textHead and textTail never split an encoded rune at the byte budget.
func textHead(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func textTail(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	start := len(value) - limit
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}
