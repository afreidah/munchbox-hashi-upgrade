// -------------------------------------------------------------------------------
// Run Storage Tests - Round Trip, Schema Gating, Replacement Durability
//
// Author: Alex Freidah
//
// Exercised against a real filesystem rather than an abstraction over one,
// because what is worth proving here are filesystem properties: that a write
// which cannot complete leaves the previous run readable, that repeated saves
// leave nothing staged behind, and that a file holding cluster layout is not
// written wider than the operator.
// -------------------------------------------------------------------------------

package plan_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// populated builds a run bound to path with every optional field set, so a
// round trip that drops one is visible rather than passing by omission.
func populated(path string) *plan.Run {
	surveyed := time.Date(2026, time.September, 22, 10, 0, 0, 0, time.UTC)

	r := plan.Create(path, "1.2.3",
		plan.Spec{Tool: plan.Nomad, From: "2.0.5", To: "2.0.6", Drain: true},
		plan.Cluster{
			SurveyedAt: surveyed,
			Members: []plan.Member{
				{
					ID: "m-1", Name: "server-a", Addr: "10.0.0.1",
					Kind: plan.KindServer, Version: "2.0.5", Status: "alive",
					Primary: true, Voter: true,
					Labels: map[string]string{"role": "edge"},
				},
				{ID: "m-2", Name: "client-a", Addr: "10.0.0.2", Kind: plan.KindClient, Version: "2.0.5"},
			},
		},
		[]plan.Task{{
			ID:           "upgrade-server-a",
			Title:        "Upgrade server-a",
			Stage:        plan.StageServers,
			Member:       "server-a",
			Action:       plan.Action{Command: "upgrade-member", Args: map[string]any{"member": "server-a"}},
			Confirm:      plan.ConfirmTyped,
			Irreversible: true,
			Checks:       map[string]any{"voting": true, "commits_behind": 0},
		}},
	)

	started := surveyed.Add(time.Minute)
	r.Begin("upgrade-server-a", started)
	r.Settle("upgrade-server-a", plan.Succeeded, started.Add(90*time.Second), nil)
	return r
}

func TestSaveAndOpen_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "run.yaml")

	if err := populated(path).Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := plan.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if got.Spec != (plan.Spec{Tool: plan.Nomad, From: "2.0.5", To: "2.0.6", Drain: true}) {
		t.Errorf("Spec = %+v, want it preserved", got.Spec)
	}
	if len(got.Tasks) != 1 {
		t.Fatalf("Tasks = %d, want 1", len(got.Tasks))
	}
	task := got.Tasks[0]
	if !task.Irreversible {
		t.Error("Irreversible was lost in the round trip")
	}
	if task.Confirm != plan.ConfirmTyped {
		t.Errorf("Confirm = %q, want %q", task.Confirm, plan.ConfirmTyped)
	}
	if got.Outcome("upgrade-server-a") != plan.Succeeded {
		t.Errorf("Outcome = %q, want progress to survive", got.Outcome("upgrade-server-a"))
	}
	if rec := got.Progress["upgrade-server-a"]; rec.StartedAt == nil || rec.FinishedAt == nil {
		t.Errorf("record timestamps lost: %+v", rec)
	}
	if len(got.Cluster.Members) != 2 || !got.Cluster.Members[0].Primary {
		t.Errorf("Cluster did not survive: %+v", got.Cluster)
	}
	if got.Cluster.Members[0].Labels["role"] != "edge" {
		t.Errorf("Labels = %v, want the survey values preserved", got.Cluster.Members[0].Labels)
	}
}

// Open binds the run to the file it came from, so the caller that resumes it
// can save it back without carrying the path alongside.
func TestOpen_BindsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.yaml")
	if err := populated(path).Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := plan.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got.Path() != path {
		t.Errorf("Path() = %q, want %q", got.Path(), path)
	}

	got.Settle("upgrade-server-a", plan.Failed, time.Now().UTC(), errors.New("later failure"))
	if err := got.Save(); err != nil {
		t.Fatalf("Save() after Open error = %v", err)
	}

	again, err := plan.Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	if again.Outcome("upgrade-server-a") != plan.Failed {
		t.Error("Save() after Open did not return the run to the same file")
	}
}

// A run assembled directly rather than through Create or Open has nowhere to
// go, and must say so instead of writing somewhere arbitrary.
func TestSave_WithoutPath(t *testing.T) {
	err := (&plan.Run{Schema: plan.Schema}).Save()
	if err == nil {
		t.Fatal("Save() error = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "no path") {
		t.Errorf("error = %v, want it to name the missing path", err)
	}
}

func TestOpen_RejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.yaml")
	if err := os.WriteFile(path, []byte("schema: 99\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := plan.Open(path); !errors.Is(err, plan.ErrSchema) {
		t.Errorf("error = %v, want ErrSchema", err)
	}
}

// A file with no schema key decodes as zero, which is not this build's version
// and must be refused rather than treated as current.
func TestOpen_RejectsMissingSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.yaml")
	if err := os.WriteFile(path, []byte("built_by: 1.2.3\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := plan.Open(path); !errors.Is(err, plan.ErrSchema) {
		t.Errorf("error = %v, want ErrSchema", err)
	}
}

func TestOpen_RejectsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.yaml")
	if err := os.WriteFile(path, []byte("tasks: [unclosed\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := plan.Open(path); err == nil {
		t.Error("Open() error = nil, want a parse failure")
	}
}

func TestOpen_MissingFile(t *testing.T) {
	if _, err := plan.Open(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("Open() error = nil, want a read failure")
	}
}

// A run written with no progress still has to accept recording once resumed,
// so Open must not hand back a nil map.
func TestOpen_ProgressAlwaysWritable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.yaml")
	fresh := plan.Create(path, "1.2.3", plan.Spec{Tool: plan.Nomad}, plan.Cluster{}, tasks("a"))
	if err := fresh.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := plan.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	got.Settle("a", plan.Succeeded, time.Now().UTC(), nil)

	if got.Outcome("a") != plan.Succeeded {
		t.Error("recording against a reopened run did not take")
	}
}

// The run records cluster layout, so neither it nor a directory created for it
// may be readable beyond the operator.
func TestSave_RestrictsPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")
	path := filepath.Join(dir, "run.yaml")

	if err := populated(path).Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(run) error = %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("run mode = %o, want 600", perm)
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(dir) error = %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("directory mode = %o, want 700", perm)
	}
}

// Saving after every task is the common path, so replacing an existing run has
// to work repeatedly and leave nothing staged behind.
func TestSave_ReplacesWithoutLitter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.yaml")

	r := populated(path)
	for range 3 {
		if err := r.Save(); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "run.yaml" {
		t.Errorf("directory holds %v, want only the run", entries)
	}
}

// The whole reason for staging a sibling file: a save that cannot complete
// must not destroy the run already on disk.
func TestSave_PreservesExistingRunOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.yaml")

	if err := populated(path).Save(); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}

	// A read-only directory fails staging, the earliest point a save can fail
	// and the one that most threatens the existing file.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	doomed := populated(path)
	doomed.Spec.To = "9.9.9"
	if err := doomed.Save(); err == nil {
		t.Fatal("Save() error = nil, want a failure on a read-only directory")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("Chmod() restore error = %v", err)
	}
	got, err := plan.Open(path)
	if err != nil {
		t.Fatalf("Open() after failed save error = %v", err)
	}
	if got.Spec.To != "2.0.6" {
		t.Errorf("Spec.To = %q, want the original run to survive", got.Spec.To)
	}
}

// A directory cannot be created where a plain file already sits, and that has
// to surface rather than panic or write somewhere else.
func TestSave_DirectoryPathIsAFile(t *testing.T) {
	root := t.TempDir()
	collision := filepath.Join(root, "runs")
	if err := os.WriteFile(collision, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := populated(filepath.Join(collision, "run.yaml")).Save()
	if err == nil {
		t.Fatal("Save() error = nil, want a failure creating the run directory")
	}
	if !strings.Contains(err.Error(), "create run directory") {
		t.Errorf("error = %v, want it to name the directory creation", err)
	}
}

// The file is reviewed by an operator before it is applied, so the rendered
// keys are part of the interface. yaml.v3 folds an untagged multi-word field
// into one word, so this pins the readable spelling and catches a field added
// later without a tag.
func TestSave_RendersReadableKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.yaml")
	if err := populated(path).Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	//nolint:gosec // reading the file the test just wrote
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)

	for _, key := range []string{
		"schema:", "built_by:", "created_at:", "surveyed_at:", "started_at:", "finished_at:",
	} {
		if !strings.Contains(got, key) {
			t.Errorf("rendered run is missing key %q", key)
		}
	}
	for _, folded := range []string{"surveyedat:", "createdat:", "startedat:", "finishedat:", "builtby:"} {
		if strings.Contains(got, folded) {
			t.Errorf("rendered run contains folded key %q; the field needs a yaml tag", folded)
		}
	}
}

// Most workers carry neither a leader flag nor labels worth listing; emitting
// an empty value for every one of them buries the members that do.
func TestSave_OmitsEmptyMemberFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.yaml")
	r := plan.Create(path, "1.2.3", plan.Spec{Tool: plan.Nomad}, plan.Cluster{
		Members: []plan.Member{{ID: "m-1", Name: "client-a", Kind: plan.KindClient}},
	}, nil)

	if err := r.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	//nolint:gosec // reading the file the test just wrote
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got := string(data)

	for _, empty := range []string{"status: \"\"", "labels: {}", "primary: false", "voter: false", "drain: false"} {
		if strings.Contains(got, empty) {
			t.Errorf("rendered run contains %q; it should be omitted when unset", empty)
		}
	}
}
