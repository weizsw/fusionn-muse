package executor

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/fusionn-muse/pkg/logger"
)

const (
	dimStart = "\033[2m"
	dimEnd   = "\033[0m"
)

// StreamDimmed reads from r, writes to buf for capture, and prints dimmed to stderr.
// This creates a Docker-build-like experience where script output is visible but greyed out.
func StreamDimmed(wg *sync.WaitGroup, r io.Reader, buf *bytes.Buffer) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	// Increase buffer for potentially long lines
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		buf.WriteString(line)
		buf.WriteByte('\n')
		// Print dimmed to stderr (doesn't interfere with structured logs)
		fmt.Fprintf(os.Stderr, "%s  │ %s%s\n", dimStart, line, dimEnd)
	}

	if err := scanner.Err(); err != nil {
		logger.Debugf("Scanner error (may be normal): %v", err)
	}
}
