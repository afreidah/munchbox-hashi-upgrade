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

	return out.String(), func() error { _, err := root.ExecuteC(); return err }()
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

// The file is the journal: a no-op settles every task and writes the outcome
// back, so the same file reopened reports the run as finished.
func TestNoOpWorksThroughTheFileAndRecordsProgress(t *testing.T) {
	path := written(t, runFile)

	out, err := execRun(t, "--no-op", path)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}

	run, err := plan.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !run.Finished() {
		t.Error("the run did not finish")
	}
	for _, task := range run.Tasks {
		if got := run.Outcome(task.ID); got != plan.Unnecessary {
			t.Errorf("%s outcome = %q, want %q", task.ID, got, plan.Unnecessary)
		}
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

	// Settle both tasks, then fail the second, which is what a run that broke
	// on the pin would have left behind.
	if _, err := execRun(t, "--no-op", path); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	run, err := plan.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	run.Settle("set-version-pin", plan.Failed, time.Now().UTC(), errors.New("boom"))
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

	// With it, the task is reached again and the run finishes.
	if out, err := execRun(t, "--no-op", "--reset", "set-version-pin", path); err != nil {
		t.Fatalf("run after reset: %v\n%s", err, out)
	}

	after, err := plan.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !after.Finished() {
		t.Error("the run did not finish after the reset")
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
