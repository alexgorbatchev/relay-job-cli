// Package display formats values for terminal output without changing stored data.
package display

import (
	"os"
	"strings"
)

// Path folds home-directory paths, including paths inside diagnostic messages.
func Path(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s
	}
	var out strings.Builder
	for offset := 0; ; {
		i := strings.Index(s[offset:], home)
		if i < 0 {
			out.WriteString(s[offset:])
			return out.String()
		}
		i += offset
		end := i + len(home)
		out.WriteString(s[offset:i])
		before := i == 0 || strings.ContainsRune(" \t\r\n\"'=:([", rune(s[i-1]))
		after := end == len(s) || s[end] == '/' || strings.ContainsRune(" \t\r\n\"':)]", rune(s[end]))
		if before && after {
			out.WriteByte('~')
		} else {
			out.WriteString(home)
		}
		offset = end
	}
}
