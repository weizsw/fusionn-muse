package version

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Version holds the current build version. Override with
// -ldflags "-X github.com/fusionn-muse/internal/version.Version=v1.2.3".
var Version = "dev"

const (
	separator = "────────────────────────────────────────────────────────────"
	banner    = `
   ___           _                                                
  / _|_   _ ___(_) ___  _ __  _ __        _ __ ___  _   _ ___  ___ 
 | |_| | | / __| |/ _ \| '_ \| '_ \ _____| '_ ' _ \| | | / __|/ _ \
 |  _| |_| \__ \ | (_) | | | | | | |_____| | | | | | |_| \__ \  __/
 |_|  \__,_|___/_|\___/|_| |_|_| |_|     |_| |_| |_|\__,_|___/\___|
`
)

// Banner returns the ASCII-art project banner.
func Banner() string {
	return strings.Trim(banner, "\n")
}

// PrintBanner writes the decorated banner and version info to w (stdout if nil).
func PrintBanner(w io.Writer) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, separator)
	fmt.Fprintln(w, Banner())
	fmt.Fprintf(w, "\n  fusionn-muse %s\n", Version)
	fmt.Fprintf(w, "  Media Library Intelligence Service\n")
	fmt.Fprintln(w, separator)
	fmt.Fprintln(w)
}
