package acp

import (
	"io"
	"strings"
	"sync"
)

// filterACPEnv strips sensitive env vars from the subprocess environment.
func filterACPEnv(environ []string) []string {
	var filtered []string
	for _, e := range environ {
		key, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(key)
		switch {
		case strings.HasPrefix(upper, "GOCLAW"):
			continue
		case strings.HasPrefix(upper, "CLAUDE"):
			continue
		case strings.HasPrefix(upper, "ANTHROPIC"):
			continue
		case strings.HasPrefix(upper, "OPENAI"):
			continue
		case strings.HasPrefix(upper, "DATABASE"):
			continue
		case upper == "DB_DSN" || upper == "PGPASSWORD":
			continue
		default:
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// limitedWriter captures up to max bytes of output for diagnostics.
type limitedWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.max - len(w.buf)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		w.buf = append(w.buf, p...)
	}
	return len(p), nil
}

func (w *limitedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// Ensure limitedWriter satisfies io.Writer.
var _ io.Writer = (*limitedWriter)(nil)
