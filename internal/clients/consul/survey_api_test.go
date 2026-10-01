// -------------------------------------------------------------------------------
// Survey API Tests - Endpoint Wiring and Failure Reporting
//
// Author: Alex Freidah
//
// Drives the reads against a stand-in datacenter so the calls, their query
// construction and their error wrapping are exercised as the client actually
// issues them. What the results are reconciled into is covered separately,
// against assemble.
//
// The unhealthy case is here rather than assumed: Consul answers 429 for a
// datacenter that is not well, and this client is expected to read that as the
// answer it is.
// -------------------------------------------------------------------------------

package consul_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/consul/api"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/consul"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

const (
	healthPath   = "/v1/operator/autopilot/health"
	membersPath  = "/v1/agent/members"
	selfPath     = "/v1/agent/self"
	transferPath = "/v1/operator/raft/transfer-leader"
)

// answers configures the stand-in: which paths fail, and whether the
// datacenter reports itself unwell.
type answers struct {
	fail   map[string]bool
	unwell bool
}

// datacenter stands in for Consul, answering the endpoints the client reads.
func datacenter(t *testing.T, a answers) *consul.Consul {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc(healthPath, func(w http.ResponseWriter, _ *http.Request) {
		if a.fail[healthPath] {
			http.Error(w, "autopilot unavailable", http.StatusInternalServerError)
			return
		}

		reply := api.OperatorHealthReply{
			Healthy:          !a.unwell,
			FailureTolerance: 1,
			Servers: []api.ServerHealth{{
				ID: "s-1", Name: "alpha", Address: "10.0.0.1:8300",
				SerfStatus: "alive", Version: "2.0.3",
				Leader: true, Voter: true, Healthy: !a.unwell,
			}},
		}

		// How Consul says a datacenter is not well. The client is expected to
		// decode it rather than treat it as a failure.
		if a.unwell {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
		}
		writeJSON(t, w, reply)
	})

	mux.HandleFunc(membersPath, func(w http.ResponseWriter, _ *http.Request) {
		if a.fail[membersPath] {
			http.Error(w, "members unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(t, w, []*api.AgentMember{
			{Name: "alpha", Addr: "10.0.0.1", Status: 1, Tags: map[string]string{"role": "consul", "build": "2.0.3:abc"}},
			{Name: "bravo", Addr: "10.0.0.2", Status: 1, Tags: map[string]string{"role": "node", "build": "2.0.3:abc"}},
		})
	})

	mux.HandleFunc(selfPath, func(w http.ResponseWriter, _ *http.Request) {
		if a.fail[selfPath] {
			http.Error(w, "agent unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(t, w, map[string]map[string]any{"Config": {"Datacenter": "munchbox"}})
	})

	mux.HandleFunc(transferPath, func(w http.ResponseWriter, _ *http.Request) {
		if a.fail[transferPath] {
			http.Error(w, "transfer refused", http.StatusInternalServerError)
			return
		}
		writeJSON(t, w, api.TransferLeaderResponse{Success: true})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	c, err := consul.New(consul.Options{Address: strings.TrimPrefix(server.URL, "http://")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

// -------------------------------------------------------------------------
// READS
// -------------------------------------------------------------------------

func TestHealth(t *testing.T) {
	cluster, err := datacenter(t, answers{}).Health(t.Context())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	if !cluster.Healthy || cluster.Tolerance != 1 {
		t.Errorf("cluster = healthy %v tolerance %d, want true and 1", cluster.Healthy, cluster.Tolerance)
	}
	if got := len(cluster.OfKind(plan.KindServer)); got != 1 {
		t.Errorf("servers = %d, want 1", got)
	}
	if got := len(cluster.OfKind(plan.KindClient)); got != 1 {
		t.Errorf("clients = %d, want 1: the server also gossips and must not be counted twice", got)
	}
}

// Health carries no name. It is read between every step of a run and what the
// datacenter calls itself does not change while one is in progress.
func TestHealthCarriesNoName(t *testing.T) {
	cluster, err := datacenter(t, answers{}).Health(t.Context())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if cluster.Name != "" {
		t.Errorf("Name = %q, want empty", cluster.Name)
	}
}

func TestSurveyNamesTheDatacenter(t *testing.T) {
	cluster, err := datacenter(t, answers{}).Survey(t.Context())
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}
	if cluster.Name != "munchbox" {
		t.Errorf("Name = %q, want munchbox", cluster.Name)
	}
}

// An unwell datacenter is an answer, not a failure: it is a condition a run
// waits out, and the gates read it.
func TestHealthOfAnUnwellDatacenter(t *testing.T) {
	cluster, err := datacenter(t, answers{unwell: true}).Health(t.Context())
	if err != nil {
		t.Fatalf("Health of an unwell datacenter: %v", err)
	}
	if cluster.Healthy {
		t.Error("Healthy = true, want false")
	}
}

// -------------------------------------------------------------------------
// FAILURES
// -------------------------------------------------------------------------

func TestReadsThatFail(t *testing.T) {
	cases := map[string]struct {
		path string
		call func(*consul.Consul) error
	}{
		"autopilot": {healthPath, func(c *consul.Consul) error {
			_, err := c.Health(t.Context())
			return err
		}},
		"members": {membersPath, func(c *consul.Consul) error {
			_, err := c.Health(t.Context())
			return err
		}},
		"agent config": {selfPath, func(c *consul.Consul) error {
			_, err := c.Survey(t.Context())
			return err
		}},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dc := datacenter(t, answers{fail: map[string]bool{c.path: true}})
			if err := c.call(dc); err == nil {
				t.Error("expected an error when the read fails")
			}
		})
	}
}

// -------------------------------------------------------------------------
// LEADERSHIP
// -------------------------------------------------------------------------

// Consul names the destination optionally, so an empty id is the ordinary
// call: what matters is that the host being restarted stops leading.
func TestHandoff(t *testing.T) {
	if err := datacenter(t, answers{}).Handoff(t.Context(), ""); err != nil {
		t.Fatalf("Handoff: %v", err)
	}
}

func TestHandoffThatIsRefused(t *testing.T) {
	dc := datacenter(t, answers{fail: map[string]bool{transferPath: true}})

	if err := dc.Handoff(t.Context(), "id-alpha"); err == nil {
		t.Error("expected an error when the transfer is refused")
	}
}
