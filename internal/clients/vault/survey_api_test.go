// -------------------------------------------------------------------------------
// Survey API Tests - Endpoint Wiring and Failure Reporting
//
// Author: Alex Freidah
//
// Drives the reads against a stand-in cluster so the calls, the per-node
// addressing and the error wrapping are exercised as the client actually issues
// them. What the results are reconciled into is covered separately, against
// assemble.
//
// The per-node read is the part that needs a server to prove: a node is asked
// about itself at its own API address, which means the client has to re-point
// at an address the HA status gave it. A stand-in that serves both the status
// and the nodes is the only way to see that happen.
// -------------------------------------------------------------------------------

package vault_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/vault"
)

const (
	statusPath   = "/v1/sys/ha-status"
	healthPath   = "/v1/sys/health"
	stepDownPath = "/v1/sys/step-down"
)

// answers configures the stand-in: which paths fail, and what each node says
// about itself.
type answers struct {
	statusFails   bool
	healthFails   bool
	stepDownFails bool
	sealed        bool
	// Addresses the HA status advertises. Empty means the stand-in advertises
	// itself, which is the usual case.
	addresses []string
}

// cluster starts a stand-in Vault and returns a client pointed at it, along
// with a record of which paths were asked for.
func cluster(t *testing.T, a answers) (*vault.Vault, *[]string) {
	t.Helper()

	// The library retries a 5xx with backoff, which is wanted against a real
	// cluster mid-restart and is only latency here. The cases below assert what
	// a failed read is reported as, not how many times it was attempted.
	t.Setenv("VAULT_MAX_RETRIES", "0")

	var asked []string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc(statusPath, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if a.statusFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		addrs := a.addresses
		if addrs == nil {
			addrs = []string{srv.URL}
		}
		nodes := make([]map[string]any, 0, len(addrs))
		for i, addr := range addrs {
			nodes = append(nodes, map[string]any{
				"hostname":    "node-" + string(rune('a'+i)),
				"api_address": addr,
				"active_node": i == 0,
				"version":     "2.0.4",
			})
		}
		writeJSON(t, w, map[string]any{"nodes": nodes})
	})

	mux.HandleFunc(healthPath, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if a.healthFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(t, w, map[string]any{
			"initialized":  true,
			"sealed":       a.sealed,
			"standby":      false,
			"version":      "2.0.4",
			"cluster_name": "munchbox-vault",
		})
	})

	mux.HandleFunc(stepDownPath, func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if a.stepDownFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	client, err := vault.New(vault.Options{Address: srv.URL})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return client, &asked
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func asks(asked []string, path string) bool {
	for _, p := range asked {
		if p == path {
			return true
		}
	}
	return false
}

// -------------------------------------------------------------------------
// READING THE CLUSTER
// -------------------------------------------------------------------------

// Both reads are issued: the HA set, then each node about itself.
func TestSurveyReadsTheStatusAndThenEachNode(t *testing.T) {
	client, asked := cluster(t, answers{})

	got, err := client.Survey(context.Background())
	if err != nil {
		t.Fatalf("survey: %v", err)
	}

	if !asks(*asked, statusPath) || !asks(*asked, healthPath) {
		t.Errorf("asked %v, want both the status and a node health read", *asked)
	}
	if len(got.Members) != 1 {
		t.Fatalf("members = %d, want 1", len(got.Members))
	}
	if got.Name != "munchbox-vault" {
		t.Errorf("name = %q, want the name the node reports", got.Name)
	}
}

// Health reads the same way. It is called between every step of a run, so it
// has to stand on its own rather than depending on a survey having run.
func TestHealthReadsWithoutASurvey(t *testing.T) {
	client, _ := cluster(t, answers{})

	got, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !got.Healthy || len(got.Members) != 1 {
		t.Errorf("health gave %+v", got)
	}
}

// A sealed node comes back through the real client path, not just through
// assemble: the status code Vault uses for sealed is one the library has to
// force into range for the body to parse at all.
func TestASealedNodeIsReadThroughTheClient(t *testing.T) {
	client, _ := cluster(t, answers{sealed: true})

	got, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if got.Healthy {
		t.Error("a cluster whose only node is sealed reads as healthy")
	}
	if got.Members[0].Status != "sealed" {
		t.Errorf("status = %q, want sealed", got.Members[0].Status)
	}
}

// Each node is asked at the address the status gave for it, so a cluster of
// several nodes is several reads rather than one repeated against the entry
// point.
func TestEachNodeIsAskedAtItsOwnAddress(t *testing.T) {
	var reached []string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.Host)
		writeJSON(t, w, map[string]any{
			"initialized": true, "standby": true, "version": "2.0.4",
		})
	}))
	t.Cleanup(node.Close)

	client, _ := cluster(t, answers{addresses: []string{node.URL, node.URL}})

	got, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if len(got.Members) != 2 {
		t.Fatalf("members = %d, want 2", len(got.Members))
	}
	if len(reached) != 2 {
		t.Errorf("reached the node %d times, want one read per node", len(reached))
	}
}

// -------------------------------------------------------------------------
// WHEN A READ FAILS
// -------------------------------------------------------------------------

// Without the HA status there is no cluster to describe, so the read stops.
func TestAFailedStatusReadStopsTheSurvey(t *testing.T) {
	client, _ := cluster(t, answers{statusFails: true})

	_, err := client.Survey(context.Background())
	if err == nil {
		t.Fatal("a failed status read was not reported")
	}
	if !strings.Contains(err.Error(), "ha status") {
		t.Errorf("err = %v, want it to name the read that failed", err)
	}
}

// A node that will not answer is recorded as unreachable rather than failing
// the read. A run is often called for a cluster with one node down, and a
// survey that refuses to describe it would leave nothing to act on.
func TestANodeThatWillNotAnswerStillAppears(t *testing.T) {
	client, _ := cluster(t, answers{healthFails: true})

	got, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if len(got.Members) != 1 {
		t.Fatalf("members = %d, want the node to still be listed", len(got.Members))
	}
	// It served the status, so it is the active node whatever its own read did.
	if got.Members[0].Status != "active" {
		t.Errorf("status = %q, want active for the node that served the status", got.Members[0].Status)
	}
}

// A node advertising no address cannot be asked about itself, which is a gap in
// the answer rather than a reason to fail.
func TestANodeWithNoAddressIsNotFatal(t *testing.T) {
	client, _ := cluster(t, answers{addresses: []string{""}})

	got, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if len(got.Members) != 1 {
		t.Errorf("members = %d, want 1", len(got.Members))
	}
}

// -------------------------------------------------------------------------
// STEPPING DOWN
// -------------------------------------------------------------------------

// The id is accepted and ignored: Vault offers no way to name a successor, and
// the step that calls this passes the one it picked for the other tools.
func TestHandoffStepsDownAndIgnoresTheSuccessor(t *testing.T) {
	client, asked := cluster(t, answers{})

	if err := client.Handoff(context.Background(), "node-b"); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if !asks(*asked, stepDownPath) {
		t.Errorf("asked %v, want a step-down", *asked)
	}
}

// An empty id is the same request, since the id was never used.
func TestHandoffWithNoSuccessorStepsDownAnyway(t *testing.T) {
	client, asked := cluster(t, answers{})

	if err := client.Handoff(context.Background(), ""); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if !asks(*asked, stepDownPath) {
		t.Errorf("asked %v, want a step-down", *asked)
	}
}

func TestAFailedStepDownIsReported(t *testing.T) {
	client, _ := cluster(t, answers{stepDownFails: true})

	err := client.Handoff(context.Background(), "")
	if err == nil {
		t.Fatal("a failed step-down was not reported")
	}
	if !strings.Contains(err.Error(), "step down") {
		t.Errorf("err = %v, want it to name the operation", err)
	}
}

// -------------------------------------------------------------------------
// CONSTRUCTION
// -------------------------------------------------------------------------

// The address is what messages use to say which cluster they mean.
func TestAddressIsWhatTheClientWasBuiltWith(t *testing.T) {
	client, err := vault.New(vault.Options{Address: "https://vault.example.test:8200"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := client.Address(); got != "https://vault.example.test:8200" {
		t.Errorf("address = %q", got)
	}
}
