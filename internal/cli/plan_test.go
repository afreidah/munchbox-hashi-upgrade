// -------------------------------------------------------------------------------
// Plan Command Tests - Generation, Naming, Summary
//
// Author: Alex Freidah
//
// Runs the command against a stand-in cluster so the whole path is exercised:
// the survey, the ordering, the file written and what an operator is shown.
// The assertions are about what reaches the operator and what lands on disk,
// since those are the command's output.
// -------------------------------------------------------------------------------

package cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/cli"
)

// stubCluster answers the three reads a survey makes. named controls whether
// the cluster publishes an identity.
func stubCluster(t *testing.T, named bool) string {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/v1/operator/autopilot/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, api.OperatorHealthReply{
			FailureTolerance: 1,
			Servers: []api.ServerHealth{
				{ID: "s-1", Name: "alpha.global", Address: "10.0.0.1:4647", Version: "2.0.5", SerfStatus: "alive", Voter: true},
				{ID: "s-2", Name: "bravo.global", Address: "10.0.0.2:4647", Version: "2.0.5", SerfStatus: "alive", Leader: true, Voter: true},
			},
		})
	})

	mux.HandleFunc("/v1/nodes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []api.NodeListStub{
			// Also a server; the survey must not count it twice.
			{ID: "n-1", Name: "alpha", Address: "10.0.0.1", Version: "2.0.5", Status: "ready"},
			{ID: "n-2", Name: "charlie", Address: "10.0.0.3", Version: "2.0.5", Status: "ready"},
		})
	})

	mux.HandleFunc("/v1/var/cluster/identity", func(w http.ResponseWriter, _ *http.Request) {
		if !named {
			http.NotFound(w, nil)
			return
		}
		writeJSON(t, w, api.Variable{
			Namespace: "default",
			Path:      "cluster/identity",
			Items:     api.VariableItems{"name": "example-cluster"},
		})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

// run executes the command tree and returns what an operator would see.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer
	root := cli.Root()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)

	err := root.ExecuteContext(t.Context())
	return out.String(), err
}

// planned returns the single run file in dir, failing if there is not exactly
// one.
func planned(t *testing.T, dir string) string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want one run file", len(entries))
	}
	return entries[0].Name()
}

func TestPlan_WritesARunAndSummarisesIt(t *testing.T) {
	dir := t.TempDir()

	out, err := run(t, "plan", "nomad", "--to", "2.0.6", "--address", stubCluster(t, true), "--dir", dir)
	if err != nil {
		t.Fatalf("plan error = %v\n%s", err, out)
	}

	name := planned(t, dir)
	if !strings.Contains(out, name) {
		t.Errorf("output does not name the file it wrote:\n%s", out)
	}

	// The summary is what an operator checks the ordering against, so the
	// sequence has to reach them, not just the file.
	for _, want := range []string{
		"example-cluster", "3 hosts", "upgrading nomad to 2.0.6",
		"Stop scheduled converges", "Pin nomad to 2.0.6",
		"Upgrade alpha", "Hand coordination off bravo", "Upgrade bravo",
		"Upgrade charlie", "Restore scheduled converges",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q:\n%s", want, out)
		}
	}
}

// The gates and the commitment point are the two things an operator most needs
// to see before applying, so they are annotated rather than left to the file.
func TestPlan_SummaryMarksGatesAndCommitment(t *testing.T) {
	out, err := run(t, "plan", "nomad", "--to", "2.0.6", "--address", stubCluster(t, true), "--dir", t.TempDir())
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}

	if !strings.Contains(out, "point of no return") {
		t.Errorf("summary does not mark the point of no return:\n%s", out)
	}
	if !strings.Contains(out, "[typed]") {
		t.Errorf("summary does not mark the tasks needing explicit assent:\n%s", out)
	}
}

// A named cluster is in the filename so two clusters planned from one working
// directory do not read alike.
func TestPlan_FilenameCarriesTheClusterName(t *testing.T) {
	dir := t.TempDir()

	if _, err := run(t, "plan", "nomad", "--to", "2.0.6", "--address", stubCluster(t, true), "--dir", dir); err != nil {
		t.Fatalf("plan error = %v", err)
	}

	name := planned(t, dir)
	if !strings.HasPrefix(name, "nomad-example-cluster-") || !strings.HasSuffix(name, ".yaml") {
		t.Errorf("filename = %q, want nomad-example-cluster-<timestamp>.yaml", name)
	}
}

// An unnamed cluster is left out of the filename rather than given a
// placeholder that would read as a name.
func TestPlan_UnnamedClusterOmittedFromFilename(t *testing.T) {
	dir := t.TempDir()

	out, err := run(t, "plan", "nomad", "--to", "2.0.6", "--address", stubCluster(t, false), "--dir", dir)
	if err != nil {
		t.Fatalf("plan error = %v", err)
	}

	name := planned(t, dir)
	if !strings.HasPrefix(name, "nomad-2") {
		t.Errorf("filename = %q, want the tool then the timestamp", name)
	}
	if !strings.Contains(out, "unnamed cluster") {
		t.Errorf("summary does not say the cluster is unnamed:\n%s", out)
	}
}

// The plan is written where it was asked for, since upgrade is handed the path
// rather than searching for it.
func TestPlan_WritesToTheRequestedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runs")

	if _, err := run(t, "plan", "nomad", "--to", "2.0.6", "--address", stubCluster(t, true), "--dir", dir); err != nil {
		t.Fatalf("plan error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, planned(t, dir))); err != nil {
		t.Errorf("Stat() error = %v, want the run in the requested directory", err)
	}
}

func TestPlan_RejectsUnknownTool(t *testing.T) {
	_, err := run(t, "plan", "terraform", "--to", "1.0.0", "--dir", t.TempDir())
	if err == nil {
		t.Fatal("error = nil, want an unknown tool to be refused")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error = %v, want it to name the problem", err)
	}
}

func TestPlan_RequiresATargetVersion(t *testing.T) {
	if _, err := run(t, "plan", "nomad", "--dir", t.TempDir()); err == nil {
		t.Fatal("error = nil, want a missing --to to be refused")
	}
}

func TestPlan_RequiresATool(t *testing.T) {
	if _, err := run(t, "plan", "--to", "2.0.6"); err == nil {
		t.Fatal("error = nil, want a missing tool to be refused")
	}
}

// A survey that reaches a cluster and finds nothing is more likely a wrong
// address or a token without permission than a genuinely empty cluster, and
// writing a run of bracket tasks against it would hide that.
func TestPlan_RefusesAnEmptySurvey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/operator/autopilot/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, api.OperatorHealthReply{})
	})
	mux.HandleFunc("/v1/nodes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []api.NodeListStub{})
	})
	mux.HandleFunc("/v1/var/cluster/identity", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	_, err := run(t, "plan", "nomad", "--to", "2.0.6", "--address", server.URL, "--dir", dir)
	if err == nil {
		t.Fatal("error = nil, want an empty survey to be refused")
	}

	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("ReadDir() error = %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("wrote %d files for a refused plan, want none", len(entries))
	}
}
