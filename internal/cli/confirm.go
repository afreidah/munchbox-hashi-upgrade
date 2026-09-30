// -------------------------------------------------------------------------------
// Confirmation - Putting A Gated Task To The Operator
//
// Author: Alex Freidah
//
// Two strengths, because two kinds of boundary. A prompt is a yes or no, for a
// task that is merely worth pausing on. A typed confirmation asks for the host's
// own name back, for the ones where assent has to cost more than a keystroke:
// moving coordination off the primary is not something to agree to by reflex on
// the third prompt in a row.
//
// Declining is not a failure. The operator has decided to stop, so the run
// reports where it got to rather than treating the answer as an error to unwind.
// -------------------------------------------------------------------------------

package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/runner"
)

// confirmer asks on a terminal.
type confirmer struct {
	in  *bufio.Scanner
	out io.Writer
}

// newConfirmer returns a Confirmer reading from in and prompting on out.
func newConfirmer(in io.Reader, out io.Writer) runner.Confirmer {
	return &confirmer{in: bufio.NewScanner(in), out: out}
}

// Confirm puts a task to the operator at the strength the task asks for.
//
// A task that wants no confirmation is not asked about at all; the runner still
// routes it here, and answering yes without printing anything keeps the
// decision in one place rather than split across the caller.
func (c *confirmer) Confirm(_ context.Context, task plan.Task) (bool, error) {
	switch task.Confirm {
	case plan.ConfirmTyped:
		return c.typed(task)
	case plan.ConfirmPrompt:
		return c.prompt(task)
	default:
		return true, nil
	}
}

// prompt asks for a yes or a no, and treats anything else as no. A run that
// stops because the answer was unclear is recoverable; one that continues is
// not necessarily.
func (c *confirmer) prompt(task plan.Task) (bool, error) {
	if task.Irreversible {
		c.sayf("  the cluster is committed past this point\n")
	}
	c.sayf("  continue? [y/N] ")

	answer, err := c.read()
	if err != nil {
		return false, err
	}
	return answer == "y" || answer == "yes", nil
}

// typed asks for the subject of the task back, so the answer cannot be given
// without having read what it is about.
func (c *confirmer) typed(task plan.Task) (bool, error) {
	want := subject(task)

	if task.Irreversible {
		c.sayf("  the cluster is committed past this point\n")
	}
	c.sayf("  type %q to continue: ", want)

	answer, err := c.read()
	if err != nil {
		return false, err
	}
	if answer != strings.ToLower(want) {
		c.sayf("  that is not %q; stopping\n", want)
		return false, nil
	}
	return true, nil
}

// sayf puts a question or a note to the operator. A write that fails is
// discarded: the answer is read from a separate stream, and a prompt that could
// not be printed is not a reason to fail the run rather than the read.
func (c *confirmer) sayf(format string, args ...any) {
	_, _ = fmt.Fprintf(c.out, format, args...)
}

// subject is what a typed confirmation asks to have typed back.
//
// The host, for a task that acts on one. Otherwise the version, because the
// task that acts on the whole fleet is the pin, and the fact worth having read
// before assenting is which version the fleet is about to converge toward.
//
// The id is a last resort and not a good one: typing a task's name back proves
// nothing except that it was on the screen.
func subject(task plan.Task) string {
	if task.Member != "" {
		return task.Member
	}
	if version, ok := task.Action.Args["version"].(string); ok && version != "" {
		return version
	}
	return task.ID
}

// read takes one line, folded and trimmed.
//
// End of input is a no rather than an error: a run driven from a pipe reaches
// a prompt it cannot ask, and stopping there is the safe reading of silence.
func (c *confirmer) read() (string, error) {
	if !c.in.Scan() {
		if err := c.in.Err(); err != nil {
			return "", err
		}
		return "", nil
	}
	return strings.ToLower(strings.TrimSpace(c.in.Text())), nil
}
