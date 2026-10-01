// -------------------------------------------------------------------------------
// Cluster Survey - The HA Set, Seal State, Derived Tolerance
//
// Author: Alex Freidah
//
// Reads what a Vault cluster is made of and returns it as the same neutral
// snapshot the other two surveys produce, so everything downstream -- ordering,
// gates, the run file -- is shared.
//
// Two differences from Nomad and Consul, both from this cluster storing its
// data in Consul rather than in raft of its own:
//
//	no voters       quorum lives in the storage backend, so no node here holds
//	                raft state and none is a voter. plan.Member already allows
//	                for a tool whose coordination does not run on its own
//	                quorum.
//	no autopilot    there is no server to ask for a failure tolerance, so it is
//	                derived: a cluster serves while one node is unsealed, which
//	                makes the tolerance the number of unsealed nodes less one.
//
// Two reads. sys/ha-status lists the HA set from the active node's point of
// view, with each node's version and which one is active. Then sys/health is
// read on each node's own API address, because that is the only thing that
// answers for a node that has just restarted: a sealed Vault is in the HA set
// and serving nothing, and a gate has to tell those apart.
// -------------------------------------------------------------------------------

package vault

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/hashicorp/vault/api"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// How long a standby may go without echoing to the active node before it is
// read as out of touch. Vault echoes on a few-second period, so a minute is
// several missed intervals rather than one unlucky sample.
const echoGrace = time.Minute

// Status values, in Vault's own vocabulary rather than normalised.
const (
	statusActive  = "active"
	statusStandby = "standby"
	statusSealed  = "sealed"
	statusLost    = "out of touch"
)

// Survey reads the cluster and returns the snapshot a run is generated against.
func (v *Vault) Survey(ctx context.Context) (plan.Cluster, error) {
	return v.read(ctx)
}

// Health reads the cluster's own verdict on itself and on each node.
//
// The same snapshot a survey is built on: it is read again between every step
// of a run, and nothing a survey adds changes while one is in progress.
func (v *Vault) Health(ctx context.Context) (plan.Cluster, error) {
	return v.read(ctx)
}

// read performs both reads and hands them to assemble.
func (v *Vault) read(ctx context.Context) (plan.Cluster, error) {
	ha, err := v.client.Sys().HAStatusWithContext(ctx)
	if err != nil {
		return plan.Cluster{}, fmt.Errorf("read ha status from %s: %w", v.addr, err)
	}
	if ha == nil {
		return plan.Cluster{}, fmt.Errorf("read ha status from %s: no nodes reported", v.addr)
	}

	// Each node is asked about itself. The active node's view of a standby is
	// an echo timestamp; only the node knows whether it is sealed, and that is
	// the difference between a host that came back and one that did not.
	health := make(map[string]*api.HealthResponse, len(ha.Nodes))
	for _, n := range ha.Nodes {
		h, err := v.nodeHealth(ctx, n.APIAddress)
		if err != nil {
			// A node that will not answer is recorded as unreachable rather
			// than failing the read: a run has to be able to see a cluster
			// with one node down, which is the state it is often called for.
			continue
		}
		health[n.Hostname] = h
	}

	return assemble(ha, health, time.Now().UTC()), nil
}

// nodeHealth reads one node's own view of itself.
//
// sys/health needs no token, and the library forces the sealed and standby
// status codes into the 2xx range so the body parses rather than becoming an
// error -- which is the whole reason this is worth asking each node directly.
func (v *Vault) nodeHealth(ctx context.Context, addr string) (*api.HealthResponse, error) {
	if addr == "" {
		return nil, fmt.Errorf("node reports no api address")
	}

	node, err := v.client.Clone()
	if err != nil {
		return nil, err
	}
	if err := node.SetAddress(addr); err != nil {
		return nil, err
	}

	return node.Sys().HealthWithContext(ctx)
}

// assemble turns the reads into a snapshot. Separate from the reads and free of
// I/O, because deciding what each node's state means is the part worth testing.
func assemble(ha *api.HAStatusResponse, health map[string]*api.HealthResponse, at time.Time) plan.Cluster {
	cluster := plan.Cluster{SurveyedAt: at}
	if ha == nil {
		return cluster
	}

	serving := 0
	for _, n := range ha.Nodes {
		h := health[n.Hostname]

		member := plan.Member{
			ID:      n.Hostname,
			Name:    n.Hostname,
			Addr:    hostOf(n.APIAddress),
			Kind:    plan.KindServer,
			Version: cmpVersion(n.Version, h),
			Primary: n.ActiveNode,
			// Voter stays false: this cluster keeps its data in Consul, so no
			// node here holds raft state and none votes on anything.
			Status: status(n, h, at),
		}
		member.Healthy = member.Status == statusActive || member.Status == statusStandby
		// Serving, because an unsealed Vault node answers requests. There is
		// nothing here to schedule onto and so nothing to be eligible for; the
		// field carries "in service" for this tool.
		member.Eligible = member.Healthy

		if member.Healthy {
			serving++
		}
		if cluster.Name == "" && h != nil {
			cluster.Name = h.ClusterName
		}

		cluster.Members = append(cluster.Members, member)
	}

	// A cluster serves while one node is unsealed and one of them is active, so
	// what it can afford to lose is every serving node but one.
	cluster.Tolerance = max(serving-1, 0)
	cluster.Healthy = serving > 0 && hasActive(cluster.Members)

	cluster.Sort()
	return cluster
}

// status names what a node is doing, preferring the node's own answer. The
// active node's view of a standby cannot see a seal, and a node that has just
// restarted is sealed for as long as it takes the KMS to answer.
func status(n api.HANode, h *api.HealthResponse, at time.Time) string {
	if h != nil {
		switch {
		case h.Sealed || !h.Initialized:
			return statusSealed
		case h.Standby:
			return statusStandby
		default:
			return statusActive
		}
	}

	// No answer from the node itself. The active node is answering by
	// definition -- it is what served the HA status -- so only a standby is in
	// doubt, and its echo says whether it is still in touch.
	if n.ActiveNode {
		return statusActive
	}
	if n.LastEcho != nil && at.Sub(*n.LastEcho) <= echoGrace {
		return statusStandby
	}
	return statusLost
}

// cmpVersion prefers what a node says about itself. The HA status carries a
// version per node, but it is the active node's record of it, and a node that
// has just been upgraded is the case where the two disagree.
func cmpVersion(reported string, h *api.HealthResponse) string {
	if h != nil && h.Version != "" {
		return h.Version
	}
	return reported
}

// hasActive is whether any node is serving as the active one. A cluster of
// unsealed standbys with no active node is up and answering nothing.
func hasActive(members []plan.Member) bool {
	for _, m := range members {
		if m.Primary && m.Status == statusActive {
			return true
		}
	}
	return false
}

// hostOf takes the host out of an API address, which Vault reports as a URL
// where the other tools report a bare address or a host and port.
func hostOf(addr string) string {
	u, err := url.Parse(addr)
	if err != nil || u.Hostname() == "" {
		return addr
	}
	return u.Hostname()
}
