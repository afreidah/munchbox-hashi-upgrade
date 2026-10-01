// -------------------------------------------------------------------------------
// Run Command Tests - Assembly, Modes And Host Resolution
//
// Author: Alex Freidah
//
// A no-op reaches nothing, which is what makes the command testable without a
// cluster: the whole path from file to runner runs, and the only thing the
// steps do is describe themselves. The live path is covered where the steps
// are, against mocks.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/execute"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

const runFile = `schema: 1
built_by: test
created_at: 2026-09-29T00:00:00Z
spec:
    tool: nomad
    from: 2.0.5
    to: 2.0.6
cluster:
    name: testcluster
    surveyed_at: 2026-09-29T00:00:00Z
    tolerance: 1
    healthy: true
    members:
        - id: aaa
          name: server-a
          addr: 10.0.0.1
          kind: server
          version: 2.0.5
        - id: bbb
          name: client-a
          addr: 10.0.0.2
          kind: client
          version: 2.0.5
tasks:
    - id: freeze-converge-timers
      title: Hold the scheduled converges
      stage: survey
      action:
        command: freeze-converge
      confirm: none
    - id: set-version-pin
      title: Point the fleet at 2.0.6
      stage: survey
      action:
        command: set-version-pin
        args:
            tool: nomad
            version: 2.0.6
      confirm: none
`

// written puts a run file in a temporary directory and returns its path.
func written(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "run.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write run file: %v", err)
	}
	return path
}

// execRun runs the command against a file and returns what it printed.
func execRun(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	root := Root()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(append([]string{"run"}, args...))

	// Run before reading. Both results are operands of one return statement and
	// are evaluated left to right, so taking the string first captured the
	// buffer before anything had been written to it.
	_, err := root.ExecuteC()

	return out.String(), err
}

// -------------------------------------------------------------------------
// MODES
// -------------------------------------------------------------------------

func TestModeReadsTheFlagsAsOneChoice(t *testing.T) {
	cases := map[string]struct {
		opts runOptions
		want execute.Mode
	}{
		"neither":    {runOptions{}, execute.Live},
		"no-op":      {runOptions{noOp: true}, execute.NoOp},
		"dry-run":    {runOptions{dryRun: true}, execute.DryRun},
		"no-op wins": {runOptions{noOp: true, dryRun: true}, execute.NoOp},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mode(c.opts); got != c.want {
				t.Errorf("mode = %q, want %q", got, c.want)
			}
		})
	}
}

// They ask for different things, so asking for both is a mistake worth
// reporting rather than resolving silently.
func TestBothModeFlagsTogetherIsRefused(t *testing.T) {
	_, err := execRun(t, "--no-op", "--dry-run", written(t, runFile))
	if err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("err = %v, want it to refuse both flags", err)
	}
}

// -------------------------------------------------------------------------
// END TO END, WITHOUT A CLUSTER
// -------------------------------------------------------------------------

// A rehearsal must leave the file as it found it.
//
// It settles its tasks in memory so the loop advances, and unnecessary counts
// as settled -- so writing that would mark the file finished, and the real run
// against it would find nothing to do and report success for an upgrade it
// never performed. That is what happened the first time this was driven
// against a live fleet.
func TestARehearsalDoesNotConsumeTheRunFile(t *testing.T) {
	for _, mode := range []string{"--no-op", "--dry-run"} {
		t.Run(mode, func(t *testing.T) {
			path := written(t, runFile)

			if out, err := execRun(t, mode, "--yes", path); err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}

			run, err := plan.Open(path)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			if run.Finished() {
				t.Error("a rehearsal left the run file finished")
			}
			for _, task := range run.Tasks {
				if got := run.Outcome(task.ID); got != plan.Waiting {
					t.Errorf("%s outcome = %q, want %q", task.ID, got, plan.Waiting)
				}
			}
		})
	}
}

// A rehearsal changes nothing, so it must not close by claiming the fleet
// moved. The same holds for a resumed run with nothing left to do.
func TestARunThatChangedNothingSaysSo(t *testing.T) {
	out, err := execRun(t, "--no-op", written(t, runFile))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if !strings.Contains(out, "nothing was changed") {
		t.Errorf("output = %q, want it to say nothing was changed", out)
	}
	if strings.Contains(out, "every host is on") {
		t.Errorf("a rehearsal claimed the fleet had moved:\n%s", out)
	}
}

// A command with no implementation stops the run by name rather than being
// stepped over, which is the difference between a run that reports what it did
// not do and one that claims a cluster it never touched.
func TestARunRefusesACommandWithNoImplementation(t *testing.T) {
	body := runFile + `    - id: from-a-later-build
      title: Something this build does not know how to do
      stage: servers
      action:
        command: reticulate-splines
      confirm: none
`

	out, err := execRun(t, "--no-op", written(t, body))
	if err == nil {
		t.Fatalf("a run with an unimplemented command succeeded\n%s", out)
	}
	if !strings.Contains(err.Error(), "reticulate-splines") {
		t.Errorf("err = %v, want it to name the command", err)
	}
}

// -------------------------------------------------------------------------
// RESETTING A TASK
// -------------------------------------------------------------------------

// A run refuses to step over a task that failed, and --reset is how an
// operator who has looked at the host puts it back into play.
func TestResetPutsATaskBackIntoPlay(t *testing.T) {
	path := written(t, runFile)

	// What a run that broke on the pin would have left behind: the freeze
	// done, the pin failed.
	run, err := plan.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Now().UTC()
	run.Settle("freeze-converge-timers", plan.Succeeded, now, nil)
	run.Settle("set-version-pin", plan.Failed, now, errors.New("boom"))
	if err := run.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Without the reset the run lands on the failure and says so.
	out, err := execRun(t, "--no-op", path)
	if err == nil {
		t.Fatalf("a run stepped over a failed task\n%s", out)
	}
	if !strings.Contains(err.Error(), "--reset set-version-pin") {
		t.Errorf("err = %v, want it to name the command that resolves it", err)
	}

	// With it, the record is cleared and the run works past the task rather
	// than landing on it.
	out, err = execRun(t, "--no-op", "--reset", "set-version-pin", path)
	if err != nil {
		t.Fatalf("run after reset: %v\n%s", err, out)
	}
	if !strings.Contains(out, "forgetting set-version-pin") {
		t.Errorf("output = %q, want it to report the reset", out)
	}

	// The reset is saved even though the rehearsal that followed it is not:
	// forgetting a task is the operator's decision about the file, not an
	// outcome of the run.
	after, err := plan.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := after.Outcome("set-version-pin"); got != plan.Waiting {
		t.Errorf("outcome = %q, want %q after the reset", got, plan.Waiting)
	}
}

// A name no task carries is a typo. Resetting nothing and carrying on would
// look like it worked.
func TestResetOfAnUnknownTaskIsRefused(t *testing.T) {
	_, err := execRun(t, "--no-op", "--reset", "no-such-task", written(t, runFile))
	if err == nil || !strings.Contains(err.Error(), "no-such-task") {
		t.Fatalf("err = %v, want it to refuse the unknown task", err)
	}
}

func TestARunFileThatDoesNotExistIsReported(t *testing.T) {
	if _, err := execRun(t, "--no-op", filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("a missing run file was accepted")
	}
}

// -------------------------------------------------------------------------
// HOST RESOLUTION
// -------------------------------------------------------------------------

// The survey's advertise address is what the cluster reaches the host on, so
// it is what the run must use too: a name resolving differently from the
// workstation would upgrade a different machine.
func TestResolverUsesTheSurveysAddress(t *testing.T) {
	cluster := plan.Cluster{Members: []plan.Member{
		{Name: "server-a", Addr: "10.0.0.1"},
		{Name: "client-a", Addr: "10.0.0.2"},
	}}
	opts := runOptions{sshUser: "ubuntu", sshPort: 2222, sshSudo: true}

	got, err := resolver(cluster, opts)("client-a")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Host != "10.0.0.2" || got.User != "ubuntu" || got.Port != 2222 || !got.Sudo {
		t.Errorf("target = %+v, want the survey address and the configured login", got)
	}
}

func TestResolverReportsAMemberTheSurveyDoesNotHold(t *testing.T) {
	cluster := plan.Cluster{Members: []plan.Member{{Name: "server-a", Addr: "10.0.0.1"}}}

	if _, err := resolver(cluster, runOptions{})("ghost-a"); err == nil {
		t.Fatal("a member outside the survey resolved")
	}
}

func TestHostsCoversEveryMember(t *testing.T) {
	cluster := plan.Cluster{Members: []plan.Member{
		{Name: "server-a", Addr: "10.0.0.1"},
		{Name: "client-a", Addr: "10.0.0.2"},
	}}

	got := hosts(cluster, runOptions{sshUser: "root", sshPort: 22})
	if len(got) != 2 {
		t.Fatalf("hosts = %d, want 2", len(got))
	}
	if got[0].Host != "10.0.0.1" || got[1].Host != "10.0.0.2" {
		t.Errorf("hosts = %+v, want them in survey order", got)
	}
}

// -------------------------------------------------------------------------
// ASSEMBLY
// -------------------------------------------------------------------------

// A no-op is given no clients at all: building them would demand credentials
// for a mode whose whole point is that it reaches nothing.
func TestNoOpIsAssembledWithoutClients(t *testing.T) {
	root := Root()
	root.SetOut(&bytes.Buffer{})

	deps, err := assemble(root, &plan.Run{}, runOptions{noOp: true})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if deps.Survey != nil || deps.Wait != nil || deps.Fleet != nil || deps.Versions != nil {
		t.Error("a no-op was given clients it cannot need")
	}
}
