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
	"cmp"
	"context"
	"fmt"
	"net"
	"slices"
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
	q := (&api.QueryOptions{}).WithContext(ctx)

	health, _, err := n.client.Operator().AutopilotServerHealth(q)
	if err != nil {
		return plan.Cluster{}, fmt.Errorf("read server health from %s: %w", n.Address(), err)
	}

	stubs, _, err := n.client.Nodes().List(q)
	if err != nil {
		return plan.Cluster{}, fmt.Errorf("list nodes from %s: %w", n.Address(), err)
	}

	name, err := n.clusterName(ctx)
	if err != nil {
		return plan.Cluster{}, err
	}

	cluster := assemble(health, stubs, time.Now().UTC())
	cluster.Name = name
	return cluster, nil
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

	servers := make(map[string]struct{}, len(health.Servers))
	for _, s := range health.Servers {
		host := hostOf(s.Address)
		servers[host] = struct{}{}
		cluster.Members = append(cluster.Members, plan.Member{
			ID:      s.ID,
			Name:    shortName(s.Name),
			Addr:    host,
			Kind:    plan.KindServer,
			Version: s.Version,
			Primary: s.Leader,
			Voter:   s.Voter,
			Status:  s.SerfStatus,
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
			ID:      s.ID,
			Name:    shortName(s.Name),
			Addr:    host,
			Kind:    plan.KindClient,
			Version: s.Version,
			Status:  s.Status,
		})
	}

	sortMembers(cluster.Members)
	return cluster
}

// sortMembers puts servers ahead of clients and orders each group by name, so
// one cluster always surveys to the same snapshot and two surveys can be
// compared. Presentation only; the order a run touches hosts in is derived
// from what they carry, not from this.
func sortMembers(members []plan.Member) {
	slices.SortStableFunc(members, func(a, b plan.Member) int {
		if a.Kind != b.Kind {
			if a.Kind == plan.KindServer {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
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
