// -------------------------------------------------------------------------------
// Prefixed Output - Whose Converge Is Speaking
//
// Author: Alex Freidah
//
// A converge streams whatever the configuration client prints, and that output
// says nothing about which host produced it. Five hosts in a row print five
// identical blocks, which reads as one host repeating itself rather than five
// hosts each doing their part -- and on a fleet of thirty it is worse than
// useless.
//
// The host is not the configuration client's to report, so it is added here.
// -------------------------------------------------------------------------------

package execute

import (
	"bytes"
	"fmt"
	"io"
)

// prefixed wraps a writer so each line it receives is labelled.
//
// Lines are assembled before being written out, because a stream arrives in
// whatever sizes the transport chose: prefixing each Write would label
// fragments rather than lines.
type prefixed struct {
	out    io.Writer
	label  string
	buffer bytes.Buffer
}

// withPrefix labels every line written to out with label.
func withPrefix(out io.Writer, label string) io.Writer {
	if out == nil {
		return io.Discard
	}
	return &prefixed{out: out, label: label}
}

// Write labels every complete line in p and holds any remainder.
//
// It reports the whole of p as written even when a fragment is still held. The
// caller's bytes have been accepted; a short count would read as a failure and
// stop a converge that is going fine.
func (w *prefixed) Write(p []byte) (int, error) {
	w.buffer.Write(p)

	for {
		line, err := w.buffer.ReadString('\n')
		if err != nil {
			// No newline yet: put the fragment back and wait for the rest.
			w.buffer.WriteString(line)
			return len(p), nil
		}

		if _, err := fmt.Fprintf(w.out, "  %s | %s", w.label, line); err != nil {
			return len(p), err
		}
	}
}

// Flush writes any line the stream ended without a newline on, so the last
// thing a converge said is not swallowed.
func (w *prefixed) Flush() error {
	if w.buffer.Len() == 0 {
		return nil
	}

	line := w.buffer.String()
	w.buffer.Reset()
	_, err := fmt.Fprintf(w.out, "  %s | %s\n", w.label, line)

	return err
}
