package security

import (
	"crypto/subtle"
	"strings"
)

// TokensEqual is shared by management and inference authentication. Neither
// credential is a fallback for the other; empty tokens never authenticate.
func TokensEqual(expected, candidate string) bool {
	return expected != "" && candidate != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(candidate)) == 1
}

func BearerToken(header string) (string, bool) {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || fields[1] == "" {
		return "", false
	}
	return fields[1], true
}
