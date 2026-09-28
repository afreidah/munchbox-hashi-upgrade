// -------------------------------------------------------------------------------
// Survey API Tests - Endpoint Wiring and Failure Reporting
//
// Author: Alex Freidah
//
// Drives Survey against a stand-in cluster so the two reads, their query
// construction and their error wrapping are exercised as the client actually
// issues them. What the results are reconciled into is covered separately,
// against assemble.
// -------------------------------------------------------------------------------

package nomad_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/nomad"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

const (
	healthPath   = "/v1/operator/autopilot/health"
	nodesPath    = "/v1/nodes"
	identityPath = "/v1/var/cluster/identity"
)

// cluster stands in for a Nomad cluster, answering the endpoints a survey
// reads and failing whichever ones the test names.
func cluster(t *testing.T, fail ...string) *nomad.Nomad {
	t.Helper()
	return newCluster(t, fail, nil)
}

// unnamedCluster answers every read but has never been given a name.
func unnamedCluster(t *testing.T) *nomad.Nomad {
	t.Helper()
	return newCluster(t, nil, []string{identityPath})
}

func newCluster(t *testing.T, fail, absent []string) *nomad.Nomad {
	t.Helper()

	mux := http.NewServeMux()
	broken := make(map[string]bool, len(fail))
	for _, p := range fail {
		broken[p] = true
	}
	unnamed := make(map[string]bool, len(absent))
	for _, p := range absent {
		unnamed[p] = true
	}

	mux.HandleFunc(healthPath, func(w http.ResponseWriter, _ *http.Request) {
		if broken[healthPath] {
			http.Error(w, "autopilot unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(t, w, api.OperatorHealthReply{
			FailureTolerance: 1,
			Servers: []api.ServerHealth{
				{ID: "s-1", Name: "alpha.global", Address: "10.0.0.1:4647", Version: "2.0.5", SerfStatus: "alive", Leader: true, Voter: true},
			},
		})
	})

	mux.HandleFunc(nodesPath, func(w http.ResponseWriter, _ *http.Request) {
		if broken[nodesPath] {
			http.Error(w, "node list unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(t, w, []api.NodeListStub{
			{ID: "n-1", Name: "bravo", Address: "10.0.0.2", Version: "2.0.5", Status: "ready"},
		})
	})

	// A cluster that has never been named answers 404 here, which is the case
	// Peek exists to make ordinary rather than exceptional.
	mux.HandleFunc(identityPath, func(w http.ResponseWriter, _ *http.Request) {
		switch {
		case broken[identityPath]:
			http.Error(w, "variables unavailable", http.StatusInternalServerError)
		case unnamed[identityPath]:
			http.NotFound(w, nil)
		default:
			writeJSON(t, w, api.Variable{
				Namespace: "default",
				Path:      "cluster/identity",
				Items:     api.VariableItems{"name": "example-cluster"},
			})
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := nomad.New(nomad.Options{Address: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func TestSurvey(t *testing.T) {
	got, err := cluster(t).Survey(t.Context())
	if err != nil {
		t.Fatalf("Survey() error = %v", err)
	}

	if got.Tolerance != 1 {
		t.Errorf("Tolerance = %d, want 1", got.Tolerance)
	}
	if len(got.Members) != 2 {
		t.Fatalf("members = %d, want a server and a client", len(got.Members))
	}
	if got.Members[0].Kind != plan.KindServer || got.Members[0].Name != "alpha" {
		t.Errorf("first member = %+v, want the server", got.Members[0])
	}
	if got.Members[1].Kind != plan.KindClient || got.Members[1].Name != "bravo" {
		t.Errorf("second member = %+v, want the client", got.Members[1])
	}
	if got.SurveyedAt.IsZero() {
		t.Error("SurveyedAt was not stamped")
	}
	if got.Name != "example-cluster" {
		t.Errorf("Name = %q, want the cluster's published name", got.Name)
	}
}

// Health is read again between every step of a run, and the name is read once.
// A cluster whose variable endpoint is broken still gates, which is what the
// split is for: the gates poll the two reads they need and nothing else.
func TestHealth_SkipsTheClusterName(t *testing.T) {
	c := cluster(t, identityPath)

	got, err := c.Health(t.Context())
	if err != nil {
		t.Fatalf("Health() error = %v, want the name read left alone", err)
	}
	if got.Name != "" {
		t.Errorf("Name = %q, want Health to leave it unread", got.Name)
	}
	if len(got.Members) != 2 {
		t.Errorf("members = %d, want the server and the client", len(got.Members))
	}

	if _, err := c.Survey(t.Context()); err == nil {
		t.Error("Survey() error = nil; the same cluster should fail the name read")
	}
}

// A cluster that has never been named surveys fine and goes unnamed. Peek
// returns nothing rather than an error for a variable that was never set, and
// a missing name costs the run only a plainer filename.
func TestSurvey_UnnamedCluster(t *testing.T) {
	got, err := unnamedCluster(t).Survey(t.Context())
	if err != nil {
		t.Fatalf("Survey() error = %v, want an unnamed cluster to survey cleanly", err)
	}
	if got.Name != "" {
		t.Errorf("Name = %q, want empty", got.Name)
	}
	if len(got.Members) != 2 {
		t.Errorf("members = %d, want the survey to proceed regardless", len(got.Members))
	}
}

// Each read names itself when it fails, so an operator is told which half of
// the cluster view is missing rather than that "a request failed".
func TestSurvey_ReportsWhichReadFailed(t *testing.T) {
	cases := []struct {
		name string
		fail string
		want string
	}{
		{name: "server health", fail: healthPath, want: "read server health"},
		{name: "node list", fail: nodesPath, want: "list nodes"},
		{name: "cluster identity", fail: identityPath, want: "read cluster/identity"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cluster(t, tc.fail).Survey(t.Context())
			if err == nil {
				t.Fatal("Survey() error = nil, want the read to fail")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to name %q", err, tc.want)
			}
		})
	}
}
