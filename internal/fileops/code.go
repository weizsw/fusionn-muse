package fileops

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	hyphenatedCodePattern = regexp.MustCompile(`(?i)([a-z]{2,})-(\d{3,5})`)
	compactCodePattern    = regexp.MustCompile(`(?i)([a-z]{2,5})0*(\d{3,5})([a-z0-9]*)`)
	technicalPrefixes     = map[string]bool{
		"HD": true, "FHD": true, "UHD": true, "SD": true,
		"AVC": true, "HEVC": true, "XVID": true, "X264": true, "X265": true,
	}
)

// ExtractVideoCode returns a normalized code like SONE-269 from hyphenated or compact names.
func ExtractVideoCode(name string) (string, bool) {
	for _, match := range hyphenatedCodePattern.FindAllStringSubmatch(name, -1) {
		if code, ok := normalizeVideoCode(hyphenatedPrefix(match[1]), match[2]); ok {
			return code, true
		}
	}
	for _, match := range compactCodePattern.FindAllStringSubmatch(name, -1) {
		if code, ok := normalizeVideoCode(match[1], match[2]); ok {
			return code, true
		}
	}
	return "", false
}

func hyphenatedPrefix(prefix string) string {
	if len(prefix) <= 5 {
		return prefix
	}

	start := len(prefix)
	for start > 0 {
		ch := prefix[start-1]
		if ch < 'A' || ch > 'Z' {
			break
		}
		start--
	}
	if trailingLen := len(prefix) - start; trailingLen >= 2 && trailingLen <= 5 {
		return prefix[start:]
	}

	return prefix[len(prefix)-5:]
}

func normalizeVideoCode(prefix, digits string) (string, bool) {
	upperPrefix := strings.ToUpper(prefix)
	if technicalPrefixes[upperPrefix] {
		return "", false
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s-%03d", upperPrefix, n), true
}
