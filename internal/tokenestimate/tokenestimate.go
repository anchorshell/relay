package tokenestimate

import (
	"math"
	"regexp"
	"unicode/utf8"
)

const (
	defaultCharsPerToken = 6
	shortTokenThreshold  = 3
)

type languageRule struct {
	pattern              *regexp.Regexp
	averageCharsPerToken float64
}

var (
	whitespacePattern   = regexp.MustCompile(`^\s+$`)
	cjkPattern          = regexp.MustCompile(`[\x{4E00}-\x{9FFF}\x{3400}-\x{4DBF}\x{3000}-\x{303F}\x{FF00}-\x{FFEF}\x{30A0}-\x{30FF}\x{2E80}-\x{2EFF}\x{31C0}-\x{31EF}\x{3200}-\x{32FF}\x{3300}-\x{33FF}\x{AC00}-\x{D7AF}\x{1100}-\x{11FF}\x{3130}-\x{318F}\x{A960}-\x{A97F}\x{D7B0}-\x{D7FF}]`)
	numericPattern      = regexp.MustCompile(`^\d+(?:[.,]\d+)*$`)
	punctuationPattern  = regexp.MustCompile("[.,!?;(){}\\[\\]<>:/\\\\|@#$%^&*+=`~_-]")
	alphanumericPattern = regexp.MustCompile(`^[a-zA-Z0-9\x{00C0}-\x{00D6}\x{00D8}-\x{00F6}\x{00F8}-\x{00FF}]+$`)
	tokenSplitPattern   = regexp.MustCompile("(\\s+|[.,!?;(){}\\[\\]<>:/\\\\|@#$%^&*+=`~_-]+)")
	languageRules       = []languageRule{
		{pattern: regexp.MustCompile(`(?i)[\x{00E4}\x{00F6}\x{00FC}\x{00DF}\x{1E9E}]`), averageCharsPerToken: 3},
		{pattern: regexp.MustCompile(`(?i)[\x{00E9}\x{00E8}\x{00EA}\x{00EB}\x{00E0}\x{00E2}\x{00EE}\x{00EF}\x{00F4}\x{00FB}\x{00F9}\x{00FC}\x{00FF}\x{00E7}\x{0153}\x{00E6}\x{00E1}\x{00ED}\x{00F3}\x{00FA}\x{00F1}]`), averageCharsPerToken: 3},
		{pattern: regexp.MustCompile(`(?i)[\x{0105}\x{0107}\x{0119}\x{0142}\x{0144}\x{00F3}\x{015B}\x{017A}\x{017C}\x{011B}\x{0161}\x{010D}\x{0159}\x{017E}\x{00FD}\x{016F}\x{00FA}\x{010F}\x{0165}\x{0148}]`), averageCharsPerToken: 3.5},
	}
)

func Count(text string) int64 {
	if text == "" {
		return 0
	}
	var tokens int64
	for _, segment := range splitSegments(text) {
		tokens += estimateSegment(segment)
	}
	return tokens
}

func CountBytes(data []byte) int64 {
	if len(data) == 0 {
		return 0
	}
	return Count(string(data))
}

func CountByteLength(byteLen int64) int64 {
	if byteLen <= 0 {
		return 0
	}
	return maxInt64(1, (byteLen+defaultCharsPerToken-1)/defaultCharsPerToken)
}

func splitSegments(text string) []string {
	matches := tokenSplitPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return []string{text}
	}

	segments := make([]string, 0, len(matches)*2+1)
	start := 0
	for _, match := range matches {
		if match[0] > start {
			segments = append(segments, text[start:match[0]])
		}
		if match[1] > match[0] {
			segments = append(segments, text[match[0]:match[1]])
		}
		start = match[1]
	}
	if start < len(text) {
		segments = append(segments, text[start:])
	}
	return segments
}

func estimateSegment(segment string) int64 {
	if segment == "" || whitespacePattern.MatchString(segment) {
		return 0
	}
	if cjkPattern.MatchString(segment) {
		return int64(utf8.RuneCountInString(segment))
	}
	if numericPattern.MatchString(segment) {
		return 1
	}

	runes := utf8.RuneCountInString(segment)
	if runes <= shortTokenThreshold {
		return 1
	}
	if punctuationPattern.MatchString(segment) {
		return int64(math.Ceil(float64(runes) / 2))
	}

	charsPerToken := charsPerToken(segment)
	if alphanumericPattern.MatchString(segment) {
		return ceilTokens(runes, charsPerToken)
	}
	return ceilTokens(runes, charsPerToken)
}

func charsPerToken(segment string) float64 {
	for _, rule := range languageRules {
		if rule.pattern.MatchString(segment) {
			return rule.averageCharsPerToken
		}
	}
	return defaultCharsPerToken
}

func ceilTokens(runes int, charsPerToken float64) int64 {
	if runes <= 0 {
		return 0
	}
	if charsPerToken <= 0 {
		charsPerToken = defaultCharsPerToken
	}
	return int64(math.Ceil(float64(runes) / charsPerToken))
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
