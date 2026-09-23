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
	healthPath = "/v1/operator/autopilot/health"
	nodesPath  = "/v1/nodes"
)

// cluster stands in for a Nomad cluster, answering the two endpoints a survey
// reads and failing whichever ones the test names.
func cluster(t *testing.T, fail ...string) *nomad.Nomad {
	t.Helper()

	mux := http.NewServeMux()
	broken := make(map[string]bool, len(fail))
	for _, p := range fail {
		broken[p] = true
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
