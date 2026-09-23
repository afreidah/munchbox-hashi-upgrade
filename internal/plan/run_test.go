// -------------------------------------------------------------------------------
// Run Document Tests - Resume Point, Commitment, Survey Queries
//
// Author: Alex Freidah
//
// Covers what the runner depends on rather than the field layout: where a
// resumed run lands, whether it may still be abandoned, and the raft and
// worker partition the stage order is built from.
// -------------------------------------------------------------------------------

package plan_test

import (
	"errors"
	"testing"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

func tasks(ids ...string) []plan.Task {
	out := make([]plan.Task, 0, len(ids))
	for _, id := range ids {
		out = append(out, plan.Task{ID: id, Stage: plan.StageServers})
	}
	return out
}

func TestToolKnown(t *testing.T) {
	for _, tool := range []plan.Tool{plan.Nomad, plan.Consul, plan.Vault} {
		if !tool.Known() {
			t.Errorf("%q.Known() = false, want true", tool)
		}
	}
	for _, tool := range []plan.Tool{"", "terraform", "Nomad"} {
		if tool.Known() {
			t.Errorf("%q.Known() = true, want false", tool)
		}
	}
}

// Only success and "did not need doing" let a run move on. Failed must not
// settle, or a resumed run would step straight over the failure.
func TestOutcomeSettled(t *testing.T) {
	want := map[plan.Outcome]bool{
		plan.Waiting:     false,
		plan.Active:      false,
		plan.Succeeded:   true,
		plan.Failed:      false,
		plan.Unnecessary: true,
	}
	for outcome, expected := range want {
		if got := outcome.Settled(); got != expected {
			t.Errorf("%q.Settled() = %v, want %v", outcome, got, expected)
		}
	}
}

// A task with no record has not been reached, which is Waiting rather than an
// empty string leaking out of the map.
func TestRunOutcome_UnreachedTaskIsWaiting(t *testing.T) {
	r := plan.Create("", "test", plan.Spec{}, plan.Cluster{}, tasks("a"))
	if got := r.Outcome("a"); got != plan.Waiting {
		t.Errorf("Outcome() = %q, want %q", got, plan.Waiting)
	}
	if got := r.Outcome("never-existed"); got != plan.Waiting {
		t.Errorf("Outcome(unknown) = %q, want %q", got, plan.Waiting)
	}
}

func TestRunNext_SkipsSettled(t *testing.T) {
	now := time.Now().UTC()
	r := plan.Create("", "test", plan.Spec{}, plan.Cluster{}, tasks("a", "b", "c", "d"))
	r.Settle("a", plan.Succeeded, now, nil)
	r.Settle("b", plan.Unnecessary, now, nil)

	next, ok := r.Next()
	if !ok {
		t.Fatal("Next() returned nothing, want c")
	}
	if next.ID != "c" {
		t.Errorf("Next() = %q, want %q", next.ID, "c")
	}
}

func TestRunNext_LandsOnFailure(t *testing.T) {
	now := time.Now().UTC()
	r := plan.Create("", "test", plan.Spec{}, plan.Cluster{}, tasks("a", "b", "c"))
	r.Settle("a", plan.Succeeded, now, nil)
	r.Settle("b", plan.Failed, now, errors.New("converge failed"))

	next, ok := r.Next()
	if !ok || next.ID != "b" {
		t.Fatalf("Next() = %v, want the failed task b", next)
	}
}

// An interrupt leaves a task Active. A resumed run must stop there, because
// whether the work took effect is exactly what is unknown.
func TestRunNext_LandsOnActive(t *testing.T) {
	r := plan.Create("", "test", plan.Spec{}, plan.Cluster{}, tasks("a", "b"))
	r.Begin("a", time.Now().UTC())

	next, ok := r.Next()
	if !ok || next.ID != "a" {
		t.Fatalf("Next() = %v, want the active task a", next)
	}
}

// Next hands back a pointer into the run so a caller can read the task it is
// about to perform; a copy would silently diverge from the stored one.
func TestRunNext_PointsIntoTheRun(t *testing.T) {
	r := plan.Create("", "test", plan.Spec{}, plan.Cluster{}, tasks("a"))

	next, ok := r.Next()
	if !ok {
		t.Fatal("Next() returned nothing")
	}
	next.Title = "rewritten"

	if r.Tasks[0].Title != "rewritten" {
		t.Error("Next() returned a copy; the runner needs the stored task")
	}
}

func TestRunFinished(t *testing.T) {
	now := time.Now().UTC()
	r := plan.Create("", "test", plan.Spec{}, plan.Cluster{}, tasks("a", "b"))
	if r.Finished() {
		t.Error("Finished() = true with nothing done, want false")
	}

	r.Settle("a", plan.Succeeded, now, nil)
	r.Settle("b", plan.Unnecessary, now, nil)
	if !r.Finished() {
		t.Error("Finished() = false with everything settled, want true")
	}
}

// A run with no tasks has nothing left to do, so it reports finished rather
// than leaving the runner with neither a task nor an end.
func TestRunFinished_NoTasks(t *testing.T) {
	r := plan.Create("", "test", plan.Spec{}, plan.Cluster{}, nil)
	if !r.Finished() {
		t.Error("Finished() = false for a run with no tasks, want true")
	}
}

// Reaching an irreversible task is not the same as having crossed it, so only
// a succeeded one commits the run.
func TestRunCommitted(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		name  string
		apply func(*plan.Run)
		want  bool
	}{
		{
			name:  "not yet run",
			apply: func(*plan.Run) {},
			want:  false,
		},
		{
			name:  "started but unfinished",
			apply: func(r *plan.Run) { r.Begin("point", now) },
			want:  false,
		},
		{
			name:  "failed",
			apply: func(r *plan.Run) { r.Settle("point", plan.Failed, now, errors.New("boom")) },
			want:  false,
		},
		{
			name:  "succeeded",
			apply: func(r *plan.Run) { r.Settle("point", plan.Succeeded, now, nil) },
			want:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := plan.Create("", "test", plan.Spec{}, plan.Cluster{}, []plan.Task{
				{ID: "before"},
				{ID: "point", Irreversible: true},
			})
			tc.apply(r)
			if got := r.Committed(); got != tc.want {
				t.Errorf("Committed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunBeginAndSettle_RecordsTimesAndError(t *testing.T) {
	start := time.Date(2026, time.September, 22, 9, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)

	r := plan.Create("", "test", plan.Spec{}, plan.Cluster{}, tasks("a"))
	r.Begin("a", start)
	if got := r.Outcome("a"); got != plan.Active {
		t.Errorf("Outcome after Begin = %q, want %q", got, plan.Active)
	}

	r.Settle("a", plan.Failed, end, errors.New("converge failed"))

	rec := r.Progress["a"]
	if rec.Outcome != plan.Failed {
		t.Errorf("Outcome = %q, want %q", rec.Outcome, plan.Failed)
	}
	if rec.StartedAt == nil || !rec.StartedAt.Equal(start) {
		t.Errorf("StartedAt = %v, want %v", rec.StartedAt, start)
	}
	if rec.FinishedAt == nil || !rec.FinishedAt.Equal(end) {
		t.Errorf("FinishedAt = %v, want %v", rec.FinishedAt, end)
	}
	if rec.Err != "converge failed" {
		t.Errorf("Err = %q, want the failure recorded", rec.Err)
	}
}

// Settle keeps the start stamp Begin wrote, so a record shows how long a task
// took rather than only when it ended.
func TestRunSettle_KeepsStartStamp(t *testing.T) {
	start := time.Date(2026, time.September, 22, 9, 0, 0, 0, time.UTC)

	r := plan.Create("", "test", plan.Spec{}, plan.Cluster{}, tasks("a"))
	r.Begin("a", start)
	r.Settle("a", plan.Succeeded, start.Add(time.Minute), nil)

	if rec := r.Progress["a"]; rec.StartedAt == nil || !rec.StartedAt.Equal(start) {
		t.Errorf("StartedAt = %v, want it preserved through Settle", rec.StartedAt)
	}
}

// A run decoded from a file with no progress block still has to accept
// recording, which means the map cannot be left nil.
func TestRunSettle_OnRunWithoutProgressMap(t *testing.T) {
	r := &plan.Run{Tasks: tasks("a")}

	r.Settle("a", plan.Succeeded, time.Now().UTC(), nil)

	if got := r.Outcome("a"); got != plan.Succeeded {
		t.Errorf("Outcome = %q, want %q", got, plan.Succeeded)
	}
}

func TestClusterOfKind(t *testing.T) {
	c := plan.Cluster{Members: []plan.Member{
		{Name: "a", Kind: plan.KindServer},
		{Name: "b", Kind: plan.KindClient},
		{Name: "c", Kind: plan.KindServer},
	}}

	raft := c.OfKind(plan.KindServer)
	if len(raft) != 2 || raft[0].Name != "a" || raft[1].Name != "c" {
		t.Errorf("OfKind(raft) = %v, want a and c in survey order", raft)
	}

	workers := c.OfKind(plan.KindClient)
	if len(workers) != 1 || workers[0].Name != "b" {
		t.Errorf("OfKind(worker) = %v, want b", workers)
	}

	if got := (plan.Cluster{}).OfKind(plan.KindServer); got != nil {
		t.Errorf("OfKind on an empty survey = %v, want nil", got)
	}
}

func TestClusterLeader(t *testing.T) {
	c := plan.Cluster{Members: []plan.Member{
		{Name: "a", Kind: plan.KindServer},
		{Name: "b", Kind: plan.KindServer, Primary: true},
	}}

	leader, ok := c.Primary()
	if !ok || leader.Name != "b" {
		t.Errorf("Primary() = %v, %v; want b, true", leader, ok)
	}
}

// A survey taken during an election has no leader, which the caller has to be
// able to tell apart from a zero-valued member.
func TestClusterLeader_None(t *testing.T) {
	c := plan.Cluster{Members: []plan.Member{{Name: "a", Kind: plan.KindServer}}}

	if _, ok := c.Primary(); ok {
		t.Error("Primary() reported one during an election, want false")
	}
}
