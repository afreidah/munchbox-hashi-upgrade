// -------------------------------------------------------------------------------
// Cluster Survey - Servers, Clients, Failure Tolerance
//
// Author: Alex Freidah
//
// Reads what a cluster is made of and returns it as a neutral snapshot. Nomad
// reports its servers and its clients as two lists that overlap where a host
// runs both roles, so the reading and the reconciling both happen here rather
// than leaking a cluster's shape into the planner.
//
// The survey observes and decides nothing. Which hosts to touch, in what order
// and which to leave alone are all derived later, from the snapshot.
// -------------------------------------------------------------------------------

package nomad

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// Where a cluster publishes what it calls itself. A Nomad variable rather than
// node metadata, because metadata exists only on hosts that accept work: a
// server that accepts none carries none, and an identifier some hosts lack is
// not an identifier.
const (
	identityPath = "cluster/identity"
	identityItem = "name"
)

// Survey reads the cluster and returns the snapshot a run is generated
// against.
func (n *Nomad) Survey(ctx context.Context) (plan.Cluster, error) {
	cluster, err := n.Health(ctx)
	if err != nil {
		return plan.Cluster{}, err
	}

	name, err := n.clusterName(ctx)
	if err != nil {
		return plan.Cluster{}, err
	}

	cluster.Name = name
	return cluster, nil
}

// Health reads the cluster's own verdict on itself and on each host.
//
// The same snapshot a survey is built on, without the name: it is read again
// between every step of a run, and what the cluster calls itself does not
// change while one is in progress.
func (n *Nomad) Health(ctx context.Context) (plan.Cluster, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)

	health, _, err := n.client.Operator().AutopilotServerHealth(q)
	if err != nil {
		// An unhealthy cluster is an answer, not a failure. Autopilot reports
		// one as 429 carrying the health reply as its body, and the API client
		// treats every non-2xx as an error and discards it -- so without this
		// the unhealthy case is unreachable and every caller that reads
		// Cluster.Healthy is reading a field that can only ever be true.
		if unhealthy, ok := unhealthyReply(err); ok {
			health = unhealthy
		} else {
			return plan.Cluster{}, fmt.Errorf("read server health from %s: %w", n.Address(), err)
		}
	}

	stubs, _, err := n.client.Nodes().List(q)
	if err != nil {
		return plan.Cluster{}, fmt.Errorf("list nodes from %s: %w", n.Address(), err)
	}

	return assemble(health, stubs, time.Now().UTC()), nil
}

// unhealthyReply recovers the health reply autopilot sends with a 429.
//
// The status is how autopilot says the cluster is not healthy, which is a
// condition a run waits out rather than an error it stops for. Any other
// status, or a body that will not parse, is left as the error it was: a
// cluster that cannot be read is different from one that reads as unwell.
func unhealthyReply(err error) (*api.OperatorHealthReply, bool) {
	var resp api.UnexpectedResponseError
	if !errors.As(err, &resp) || resp.StatusCode() != http.StatusTooManyRequests {
		return nil, false
	}

	var health api.OperatorHealthReply
	if json.Unmarshal([]byte(resp.Body()), &health) != nil {
		return nil, false
	}

	return &health, true
}

// clusterName reads what the cluster calls itself, or empty when it publishes
// nothing.
//
// Peek is deliberate: a variable that has never been set comes back nil rather
// than as an error, so a cluster that has not been given a name surveys fine
// and simply goes unnamed. A genuine failure -- unreachable, or a token
// without read on the path -- still surfaces.
func (n *Nomad) clusterName(ctx context.Context) (string, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)

	v, _, err := n.client.Variables().Peek(identityPath, q)
	if err != nil {
		return "", fmt.Errorf("read %s from %s: %w", identityPath, n.Address(), err)
	}
	if v == nil {
		return "", nil
	}
	return v.Items[identityItem], nil
}

// assemble turns the two reads into a snapshot. Separate from Survey and free
// of I/O, because the reconciling is the part worth testing and the two calls
// above are not.
func assemble(health *api.OperatorHealthReply, stubs []*api.NodeListStub, at time.Time) plan.Cluster {
	cluster := plan.Cluster{SurveyedAt: at}
	if health == nil {
		return cluster
	}
	cluster.Tolerance = health.FailureTolerance
	cluster.Healthy = health.Healthy

	servers := make(map[string]struct{}, len(health.Servers))
	for _, s := range health.Servers {
		host := hostOf(s.Address)
		servers[host] = struct{}{}
		cluster.Members = append(cluster.Members, plan.Member{
			ID:          s.ID,
			Name:        shortName(s.Name),
			Addr:        host,
			Kind:        plan.KindServer,
			Version:     s.Version,
			Primary:     s.Leader,
			Voter:       s.Voter,
			Status:      s.SerfStatus,
			Healthy:     s.Healthy,
			StableSince: s.StableSince,
		})
	}

	// A host running both roles is already recorded as a server. Recording it
	// again as a client would have the run restart one raft member twice.
	for _, s := range stubs {
		host := hostOf(s.Address)
		if _, dual := servers[host]; dual {
			continue
		}
		cluster.Members = append(cluster.Members, plan.Member{
			ID:       s.ID,
			Name:     shortName(s.Name),
			Addr:     host,
			Kind:     plan.KindClient,
			Version:  s.Version,
			Status:   s.Status,
			Healthy:  s.Status == api.NodeStatusReady,
			Eligible: s.SchedulingEligibility == api.NodeSchedulingEligible,
		})
	}

	cluster.Sort()
	return cluster
}

// hostOf drops a port when one is present. The two reads report addresses
// differently, and matching them is what keeps a host running both roles from
// appearing twice.
func hostOf(addr string) string {
	addr = strings.TrimSpace(addr)
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// shortName drops the region suffix the gossip layer appends, so a host the
// server list calls "example.global" and the node list calls "example" reads
// as one name.
func shortName(name string) string {
	short, _, _ := strings.Cut(name, ".")
	return short
}
