package fileops

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	hyphenatedCodePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])([a-z]{2,5})-(\d{3,5})([^a-z0-9]|$)`)
	compactCodePattern    = regexp.MustCompile(`(?i)(^|[^a-z0-9])([a-z]{2,5})0*(\d{3,5})([a-z0-9]*)([^a-z0-9]|$)`)
	technicalPrefixes     = map[string]bool{
		"HD": true, "FHD": true, "UHD": true, "SD": true,
		"AVC": true, "HEVC": true, "XVID": true, "X264": true, "X265": true,
	}
)

// ExtractVideoCode returns a normalized code like SONE-269 from hyphenated or compact names.
func ExtractVideoCode(name string) (string, bool) {
	upper := strings.ToUpper(name)
	if match := hyphenatedCodePattern.FindStringSubmatch(upper); match != nil {
		return normalizeVideoCode(match[2], match[3])
	}
	if match := compactCodePattern.FindStringSubmatch(upper); match != nil {
		prefix := match[2]
		if technicalPrefixes[prefix] {
			return "", false
		}
		return normalizeVideoCode(prefix, match[3])
	}
	return "", false
}

func normalizeVideoCode(prefix, digits string) (string, bool) {
	n, err := strconv.Atoi(digits)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s-%03d", strings.ToUpper(prefix), n), true
}
