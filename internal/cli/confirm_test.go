// -------------------------------------------------------------------------------
// Confirmation Tests - What Each Strength Accepts
//
// Author: Alex Freidah
//
// The cases that matter are the ones where a wrong answer is taken as assent,
// so most of these are about what must NOT be read as yes: an empty line, a
// stray word, the wrong host's name, a closed pipe.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// ask runs one task through a confirmer fed with answer.
func ask(t *testing.T, task plan.Task, answer string) (bool, string) {
	t.Helper()

	var out bytes.Buffer
	ok, err := newConfirmer(strings.NewReader(answer), &out).Confirm(context.Background(), task)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	return ok, out.String()
}

// -------------------------------------------------------------------------
// PROMPT
// -------------------------------------------------------------------------

func TestPromptAccepts(t *testing.T) {
	task := plan.Task{Title: "Upgrade server-a", Confirm: plan.ConfirmPrompt}

	for _, answer := range []string{"y\n", "Y\n", "yes\n", "YES\n", " yes \n"} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			if ok, _ := ask(t, task, answer); !ok {
				t.Errorf("%q was not taken as assent", answer)
			}
		})
	}
}

// Anything that is not clearly yes stops the run. A run that stops because the
// answer was unclear is recoverable; one that continues may not be.
func TestPromptRefusesAnythingElse(t *testing.T) {
	task := plan.Task{Title: "Upgrade server-a", Confirm: plan.ConfirmPrompt}

	for _, answer := range []string{"n\n", "no\n", "\n", "sure\n", "ok\n", ""} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			if ok, _ := ask(t, task, answer); ok {
				t.Errorf("%q was taken as assent", answer)
			}
		})
	}
}

// The operator is told when they are past the point the run can walk away from,
// because that is the one prompt where the cost of yes changes.
func TestPromptSaysWhenTheClusterIsCommitted(t *testing.T) {
	task := plan.Task{Title: "Upgrade server-a", Confirm: plan.ConfirmPrompt, Irreversible: true}

	_, out := ask(t, task, "y\n")
	if !strings.Contains(out, "committed") {
		t.Errorf("output = %q, want it to warn the cluster is committed", out)
	}
}

// -------------------------------------------------------------------------
// TYPED
// -------------------------------------------------------------------------

func TestTypedAcceptsTheMembersName(t *testing.T) {
	task := plan.Task{Title: "Hand off", Member: "server-b", Confirm: plan.ConfirmTyped}

	if ok, _ := ask(t, task, "server-b\n"); !ok {
		t.Error("the member's own name was not taken as assent")
	}
}

// The whole point of a typed confirmation: yes does not work, because the
// answer has to show the operator read which host it is about.
func TestTypedRefusesAnythingButTheName(t *testing.T) {
	task := plan.Task{Title: "Hand off", Member: "server-b", Confirm: plan.ConfirmTyped}

	for _, answer := range []string{"y\n", "yes\n", "server-a\n", "\n", ""} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			if ok, _ := ask(t, task, answer); ok {
				t.Errorf("%q was taken as assent", answer)
			}
		})
	}
}

// The pin acts on the whole fleet, so it names no host. What is worth having
// read before assenting is the version, and that is what it asks for.
func TestTypedAsksForTheVersionWhenTheTaskNamesNoHost(t *testing.T) {
	task := plan.Task{
		ID:      "set-version-pin",
		Title:   "Pin nomad to 2.0.7",
		Confirm: plan.ConfirmTyped,
		Action:  plan.Action{Args: map[string]any{"version": "2.0.7"}},
	}

	ok, out := ask(t, task, "2.0.7\n")
	if !ok {
		t.Error("the version was not taken as assent")
	}
	if !strings.Contains(out, "2.0.7") {
		t.Errorf("output = %q, want it to ask for the version", out)
	}

	// The task id is on the screen either way; typing it back proves nothing.
	if ok, _ := ask(t, task, "set-version-pin\n"); ok {
		t.Error("the task id was taken as assent")
	}
}

// A task acting on the cluster with no version either has nothing better to
// ask for than its id.
func TestTypedFallsBackToTheTaskIDWhenThereIsNoMember(t *testing.T) {
	task := plan.Task{ID: "hand-off-coordination", Title: "Hand off", Confirm: plan.ConfirmTyped}

	ok, out := ask(t, task, "hand-off-coordination\n")
	if !ok {
		t.Error("the task id was not taken as assent")
	}
	if !strings.Contains(out, "hand-off-coordination") {
		t.Errorf("output = %q, want it to name what must be typed", out)
	}
}

// -------------------------------------------------------------------------
// NEITHER
// -------------------------------------------------------------------------

// A task that asks for nothing is not asked about, and nothing is printed for
// it: the run is routed through here uniformly so the decision lives in one
// place, not so every task produces a prompt.
func TestATaskThatNeedsNoConfirmationIsNotAsked(t *testing.T) {
	task := plan.Task{Title: "Release the converges", Confirm: plan.ConfirmNone}

	ok, out := ask(t, task, "")
	if !ok {
		t.Error("a task needing no confirmation was refused")
	}
	if out != "" {
		t.Errorf("output = %q, want nothing printed", out)
	}
}
