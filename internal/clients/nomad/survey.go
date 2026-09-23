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

	return assemble(health, stubs, time.Now().UTC()), nil
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
