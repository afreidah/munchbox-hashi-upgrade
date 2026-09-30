// -------------------------------------------------------------------------------
// Prefix Tests - Lines, Not Fragments
//
// Author: Alex Freidah
//
// The cases that matter are the ones a stream produces and a naive prefixer
// gets wrong: a write that carries several lines, a line split across two
// writes, and a final line with no newline on it.
// -------------------------------------------------------------------------------

package execute

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestPrefixLabelsEachLine(t *testing.T) {
	var out bytes.Buffer

	w := withPrefix(&out, "server-a")
	if _, err := io.WriteString(w, "installing\nrestarting\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	want := "  server-a | installing\n  server-a | restarting\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// A stream arrives in whatever sizes the transport chose, so a line split
// across writes must come out as one line and not two labelled fragments.
func TestPrefixHoldsAPartialLine(t *testing.T) {
	var out bytes.Buffer

	w := withPrefix(&out, "server-a")
	_, _ = io.WriteString(w, "instal")

	if out.Len() != 0 {
		t.Errorf("output = %q, want nothing until the line ends", out.String())
	}

	_, _ = io.WriteString(w, "ling\n")

	want := "  server-a | installing\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// Whatever a converge said last is often the thing worth reading, and it does
// not always end in a newline.
func TestPrefixFlushesATrailingLine(t *testing.T) {
	var out bytes.Buffer

	w := withPrefix(&out, "server-a")
	_, _ = io.WriteString(w, "converge complete")

	if err := w.(interface{ Flush() error }).Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	want := "  server-a | converge complete\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

// Reporting a short write would read as a failure and stop a converge that is
// going fine, so the whole of p is always accounted for.
func TestPrefixReportsEveryByteAccepted(t *testing.T) {
	w := withPrefix(&bytes.Buffer{}, "server-a")

	line := "a partial line with no newline"
	n, err := io.WriteString(w, line)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(line) {
		t.Errorf("wrote %d of %d bytes", n, len(line))
	}
}

func TestPrefixDiscardsWhenThereIsNowhereToWrite(t *testing.T) {
	w := withPrefix(nil, "server-a")

	if _, err := io.WriteString(w, "anything\n"); err != nil {
		t.Errorf("write to a nil writer: %v", err)
	}
}

// Several lines in one write, the last of them unterminated: the terminated
// ones go out and the fragment waits.
func TestPrefixSplitsAMixedWrite(t *testing.T) {
	var out bytes.Buffer

	w := withPrefix(&out, "client-a")
	_, _ = io.WriteString(w, "one\ntwo\nthree")

	got := out.String()
	if strings.Count(got, "\n") != 2 {
		t.Errorf("output = %q, want the two complete lines only", got)
	}
	if strings.Contains(got, "three") {
		t.Errorf("output = %q, want the fragment held back", got)
	}
}
