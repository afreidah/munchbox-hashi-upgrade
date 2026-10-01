// -------------------------------------------------------------------------------
// Runner Tests - Order, Persistence, Unwinding
//
// Author: Alex Freidah
//
// The run file is written to a temporary directory and read back, because what
// matters about persistence is what a resumed run finds on disk rather than
// what the process was holding.
// -------------------------------------------------------------------------------

package runner

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

var at = time.Date(2026, time.September, 28, 9, 0, 0, 0, time.UTC)

// tasks is a three-task run: one that acts on the fleet, one on a host, and one
// that needs assent.
func tasks() []plan.Task {
	return []plan.Task{
		{
			ID:      "freeze",
			Title:   "Stop the scheduled converges",
			Stage:   plan.StageSurvey,
			Action:  plan.Action{Command: "freeze-converge"},
			Confirm: plan.ConfirmNone,
		},
		{
			ID:      "upgrade-server-a",
			Title:   "Upgrade server-a",
			Stage:   plan.StageServers,
			Member:  "server-a",
			Action:  plan.Action{Command: "upgrade-member"},
			Confirm: plan.ConfirmNone,
		},
		{
			ID:      "handoff",
			Title:   "Hand off coordination",
			Stage:   plan.StageServers,
			Action:  plan.Action{Command: "hand-off-coordination"},
			Confirm: plan.ConfirmTyped,
		},
	}
}

// run returns a saved run in a temporary directory.
func run(t *testing.T) *plan.Run {
	t.Helper()

	r := plan.Create(
		filepath.Join(t.TempDir(), "run.yaml"),
		"test",
		plan.Spec{Tool: plan.Nomad, To: "2.0.6"},
		plan.Cluster{},
		tasks(),
	)
	if err := r.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	return r
}

// record is a step that notes it ran.
func record(ran *[]string, id string) Step {
	return func(_ context.Context, task plan.Task) (Result, error) {
		*ran = append(*ran, id)
		_ = task
		return Result{}, nil
	}
}

// build returns a runner over r, with out captured.
func build(t *testing.T, r *plan.Run, steps map[string]Step, confirm Confirmer) (*Runner, *strings.Builder) {
	t.Helper()

	// Journalling, because what these cover is a real run: the file as a
	// record of what happened is most of the behaviour being asserted. A
	// rehearsal's not writing is covered where the modes are, in the cli.
	var out strings.Builder
	runner, err := New(Options{
		Run:     r,
		Steps:   steps,
		Out:     &out,
		Confirm: confirm,
		Now:     func() time.Time { return at },
		Journal: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return runner, &out
}

// every is a step set that succeeds for all three commands, recording order.
func every(ran *[]string) map[string]Step {
	return map[string]Step{
		"freeze-converge":       record(ran, "freeze"),
		"upgrade-member":        record(ran, "upgrade"),
		"hand-off-coordination": record(ran, "handoff"),
	}
}

func TestNew(t *testing.T) {
	cases := map[string]Options{
		"no run":   {Steps: map[string]Step{"a": nil}, Out: &strings.Builder{}},
		"no steps": {Run: &plan.Run{}, Out: &strings.Builder{}},
		"no out":   {Run: &plan.Run{}, Steps: map[string]Step{"a": nil}},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(opts); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestApply(t *testing.T) {
	t.Run("runs every task in order and finishes", func(t *testing.T) {
		var ran []string
		r := run(t)
		runner, _ := build(t, r, every(&ran), nil)

		if err := runner.Apply(t.Context()); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if want := []string{"freeze", "upgrade", "handoff"}; strings.Join(ran, ",") != strings.Join(want, ",") {
			t.Errorf("ran %v, want %v", ran, want)
		}
		if !r.Finished() {
			t.Error("run did not finish")
		}
	})

	// The file is the journal. What a resumed run sees is what was written, so
	// the assertion is made against a reopened file rather than the run in hand.
	t.Run("persists each outcome as it goes", func(t *testing.T) {
		var ran []string
		r := run(t)
		runner, _ := build(t, r, every(&ran), nil)

		if err := runner.Apply(t.Context()); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		reopened, err := plan.Open(r.Path())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		for _, id := range []string{"freeze", "upgrade-server-a", "handoff"} {
			if got := reopened.Outcome(id); got != plan.Succeeded {
				t.Errorf("%s = %s on disk, want %s", id, got, plan.Succeeded)
			}
		}
		if rec := reopened.Progress["freeze"]; rec.StartedAt == nil || rec.FinishedAt == nil {
			t.Errorf("freeze timestamps = %+v, want both stamped", rec)
		}
	})

	// A step whose outcome says the work was not needed still settles, so the
	// run moves past it.
	t.Run("takes the outcome a step reports", func(t *testing.T) {
		steps := every(new([]string))
		steps["upgrade-member"] = func(context.Context, plan.Task) (Result, error) {
			return Result{Outcome: plan.Unnecessary}, nil
		}

		r := run(t)
		runner, _ := build(t, r, steps, nil)

		if err := runner.Apply(t.Context()); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got := r.Outcome("upgrade-server-a"); got != plan.Unnecessary {
			t.Errorf("upgrade-server-a = %s, want %s", got, plan.Unnecessary)
		}
	})

	t.Run("stops at a task whose command has no step", func(t *testing.T) {
		r := run(t)
		runner, _ := build(t, r, map[string]Step{"freeze-converge": record(new([]string), "freeze")}, nil)

		err := runner.Apply(t.Context())
		if !errors.Is(err, ErrNoStep) {
			t.Fatalf("Apply error = %v, want %v", err, ErrNoStep)
		}
		if got := r.Outcome("upgrade-server-a"); got != plan.Waiting {
			t.Errorf("upgrade-server-a = %s, want it untouched", got)
		}
	})

	// An interrupt stops at a task boundary. The task that was running is
	// finished and recorded first, so the file describes a whole task.
	t.Run("stops after the current task when interrupted", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())

		var ran []string
		steps := every(&ran)
		steps["freeze-converge"] = func(context.Context, plan.Task) (Result, error) {
			cancel()
			ran = append(ran, "freeze")
			return Result{}, nil
		}

		r := run(t)
		runner, out := build(t, r, steps, nil)

		if err := runner.Apply(ctx); !errors.Is(err, ErrInterrupted) {
			t.Fatalf("Apply error = %v, want %v", err, ErrInterrupted)
		}
		if strings.Join(ran, ",") != "freeze" {
			t.Errorf("ran %v, want the interrupted task alone", ran)
		}
		if got := r.Outcome("freeze"); got != plan.Succeeded {
			t.Errorf("freeze = %s, want the finished task recorded", got)
		}
		if !strings.Contains(out.String(), "stopped after freeze") {
			t.Errorf("output %q does not say where it stopped", out)
		}
	})

	// A resumed run lands on the failure instead of stepping over it, because
	// whether the failed task took effect is unknown.
	t.Run("refuses to step over a task that failed", func(t *testing.T) {
		r := run(t)
		r.Settle("freeze", plan.Failed, at, errors.New("boom"))

		runner, _ := build(t, r, every(new([]string)), nil)

		err := runner.Apply(t.Context())
		if !errors.Is(err, ErrUnsettled) {
			t.Fatalf("Apply error = %v, want %v", err, ErrUnsettled)
		}
		if !strings.Contains(err.Error(), "freeze") {
			t.Errorf("error %q does not name the task", err)
		}
	})

	// Active is what an interrupt mid-task leaves: started, never reported back.
	t.Run("refuses to retry a task left active", func(t *testing.T) {
		r := run(t)
		r.Begin("freeze", at)

		runner, _ := build(t, r, every(new([]string)), nil)

		if err := runner.Apply(t.Context()); !errors.Is(err, ErrUnsettled) {
			t.Errorf("Apply error = %v, want %v", err, ErrUnsettled)
		}
	})

	t.Run("resumes from the first unsettled task", func(t *testing.T) {
		var ran []string
		r := run(t)
		r.Settle("freeze", plan.Succeeded, at, nil)

		runner, _ := build(t, r, every(&ran), nil)

		if err := runner.Apply(t.Context()); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if strings.Join(ran, ",") != "upgrade,handoff" {
			t.Errorf("ran %v, want the settled task skipped", ran)
		}
	})
}

func TestConfirm(t *testing.T) {
	t.Run("stops when the operator declines", func(t *testing.T) {
		var ran []string
		r := run(t)
		decline := ConfirmerFunc(func(context.Context, plan.Task) (bool, error) { return false, nil })
		runner, _ := build(t, r, every(&ran), decline)

		if err := runner.Apply(t.Context()); !errors.Is(err, ErrDeclined) {
			t.Fatalf("Apply error = %v, want %v", err, ErrDeclined)
		}
		if strings.Join(ran, ",") != "freeze,upgrade" {
			t.Errorf("ran %v, want everything up to the confirmed task", ran)
		}
		if got := r.Outcome("handoff"); got != plan.Waiting {
			t.Errorf("handoff = %s, want it left unrun", got)
		}
	})

	// Only the tasks that declare a confirmation are put to the operator.
	t.Run("asks only about the tasks that ask for it", func(t *testing.T) {
		var asked []string
		ask := ConfirmerFunc(func(_ context.Context, task plan.Task) (bool, error) {
			asked = append(asked, task.ID)
			return true, nil
		})

		runner, _ := build(t, run(t), every(new([]string)), ask)
		if err := runner.Apply(t.Context()); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if strings.Join(asked, ",") != "handoff" {
			t.Errorf("asked about %v, want the confirmed task alone", asked)
		}
	})

	t.Run("reports a question that could not be asked", func(t *testing.T) {
		want := errors.New("not a terminal")
		ask := ConfirmerFunc(func(context.Context, plan.Task) (bool, error) { return false, want })

		runner, _ := build(t, run(t), every(new([]string)), ask)
		if err := runner.Apply(t.Context()); !errors.Is(err, want) {
			t.Errorf("Apply error = %v, want %v", err, want)
		}
	})
}
