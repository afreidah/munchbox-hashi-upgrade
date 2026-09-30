// -------------------------------------------------------------------------------
// Status Tests - The Three States A Run Is Read In
//
// Author: Alex Freidah
//
// A run is read to decide what to do about it, so the cases are the three
// decisions: it finished, it is partway and can carry on, or it stopped on
// something that needs a person first.
// -------------------------------------------------------------------------------

package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// opened returns a run file's contents as a run, for a test that needs to
// change its progress before reporting it.
func opened(t *testing.T, path string) *plan.Run {
	t.Helper()

	run, err := plan.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return run
}

func TestStatusOfAnUntouchedRun(t *testing.T) {
	got := report(opened(t, written(t, runFile)))

	for _, want := range []string{"nomad", "2.0.5 to 2.0.6", "waiting", "next:"} {
		if !strings.Contains(got, want) {
			t.Errorf("report does not mention %q:\n%s", want, got)
		}
	}
}

// The state a finished run is read in: nothing left, and the version it landed
// on stated rather than inferred from the absence of anything else.
func TestStatusOfAFinishedRun(t *testing.T) {
	path := written(t, runFile)
	if _, err := execRun(t, "--no-op", path); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := report(opened(t, path))
	if !strings.Contains(got, "finished") {
		t.Errorf("report does not say it finished:\n%s", got)
	}
	if strings.Contains(got, "next:") {
		t.Errorf("a finished run was given a next task:\n%s", got)
	}
}

// A task that failed is not somewhere a run carries on from, so the way past
// it is printed alongside the reason it is there.
func TestStatusOfARunStoppedOnAFailure(t *testing.T) {
	path := written(t, runFile)

	run := opened(t, path)
	run.Settle("freeze-converge-timers", plan.Failed, time.Now().UTC(), errors.New("ssh refused"))
	if err := run.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := report(opened(t, path))
	for _, want := range []string{
		"stopped",
		"freeze-converge-timers is failed",
		"ssh refused",
		"--reset freeze-converge-timers",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report does not mention %q:\n%s", want, got)
		}
	}
}

// An interrupt leaves a task started and never reported back. How long it has
// been that way is the thing worth knowing, so it is said rather than shown as
// a blank duration.
func TestStatusOfATaskThatNeverReportedBack(t *testing.T) {
	path := written(t, runFile)

	run := opened(t, path)
	run.Begin("freeze-converge-timers", time.Now().UTC().Add(-90*time.Second))
	if err := run.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := report(opened(t, path))
	if !strings.Contains(got, "never reported back") {
		t.Errorf("report does not flag the unfinished task:\n%s", got)
	}
	if !strings.Contains(got, "active") {
		t.Errorf("report does not name the outcome:\n%s", got)
	}
}

// Past the point of no return, carrying on is the safe direction, and a run
// read at 3am should say so rather than leave it to be worked out.
func TestStatusSaysWhenTheClusterIsCommitted(t *testing.T) {
	body := runFile + `    - id: upgrade-server-a
      title: Upgrade server-a
      stage: servers
      member: server-a
      action:
        command: upgrade-member
        args:
            member: server-a
      confirm: none
      irreversible: true
    - id: upgrade-server-b
      title: Upgrade server-b
      stage: servers
      member: server-b
      action:
        command: upgrade-member
        args:
            member: server-b
      confirm: none
`
	path := written(t, body)

	run := opened(t, path)
	for _, id := range []string{"freeze-converge-timers", "set-version-pin", "upgrade-server-a"} {
		run.Settle(id, plan.Succeeded, time.Now().UTC(), nil)
	}
	if err := run.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := report(opened(t, path))
	if !strings.Contains(got, "part-upgraded") {
		t.Errorf("report does not say the cluster is committed:\n%s", got)
	}
}

// A run generated against a fleet with no pin set does not know where it
// started, and saying so beats printing an empty version.
func TestStatusOfARunWithNoRecordedStart(t *testing.T) {
	body := strings.Replace(runFile, "    from: 2.0.5\n", "", 1)

	got := report(opened(t, written(t, body)))
	if !strings.Contains(got, "unrecorded") {
		t.Errorf("report does not account for the missing version:\n%s", got)
	}
}

// A fleet part-way through an upgrade is on no single version, so plan records
// none rather than one that is true of only some hosts.
func TestRunningVersionOfTheFleet(t *testing.T) {
	cases := map[string]struct {
		members []plan.Member
		want    string
	}{
		"all on one version": {
			members: []plan.Member{{Version: "2.0.5"}, {Version: "2.0.5"}},
			want:    "2.0.5",
		},
		"part way through": {
			members: []plan.Member{{Version: "2.0.5"}, {Version: "2.0.7"}},
			want:    "",
		},
		"a host reporting nothing is skipped": {
			members: []plan.Member{{Version: ""}, {Version: "2.0.5"}},
			want:    "2.0.5",
		},
		"no members": {
			members: nil,
			want:    "",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := running(plan.Cluster{Members: c.members}); got != c.want {
				t.Errorf("running = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStatusOfAFileThatDoesNotExist(t *testing.T) {
	root := Root()
	root.SetOut(&strings.Builder{})
	root.SetArgs([]string{"status", "absent.yaml"})

	if _, err := root.ExecuteC(); err == nil {
		t.Error("a missing run file was accepted")
	}
}
