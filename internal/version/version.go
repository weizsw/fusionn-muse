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

// PrintBanner writes the decorated banner, version info and the effective log
// level to w (stdout if nil). It is the one startup exception to the single-line
// event rule; the caller passes the level, so this package stays independent of
// the logger.
func PrintBanner(w io.Writer, logLevel string) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, separator)
	fmt.Fprintln(w, Banner())
	fmt.Fprintf(w, "\n  fusionn-muse %s\n", Version)
	fmt.Fprintf(w, "  Media Library Intelligence Service\n")
	fmt.Fprintf(w, "  Log level: %s\n", logLevel)
	fmt.Fprintln(w, separator)
	fmt.Fprintln(w)
}
