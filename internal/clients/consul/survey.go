// -------------------------------------------------------------------------------
// Cluster Survey - Servers, Agents, Failure Tolerance
//
// Author: Alex Freidah
//
// Reads what a datacenter is made of and returns it as the same neutral
// snapshot a Nomad survey produces, so everything downstream -- ordering,
// gates, the run file -- is shared.
//
// Two reads, as with Nomad. Autopilot describes the servers and their standing
// in raft; the gossip pool describes every agent, servers included. They are
// reconciled on name: a host already recorded as a server is not recorded again
// as an agent.
// -------------------------------------------------------------------------------

package consul

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// What the gossip pool calls a server, as against an agent that carries no
// raft state.
const serverRole = "consul"

// Tags a member carries its role and build under.
const (
	roleTag  = "role"
	buildTag = "build"
)

// Serf's status for a member that is up. The library exposes it as an int
// rather than a named constant.
const alive = 1

// Survey reads the datacenter and returns the snapshot a run is generated
// against.
//
// Consul publishes no name for itself the way Nomad does through a variable,
// so the datacenter stands in: it is what the operator calls this cluster and
// what every node agrees on.
func (c *Consul) Survey(ctx context.Context) (plan.Cluster, error) {
	cluster, err := c.Health(ctx)
	if err != nil {
		return plan.Cluster{}, err
	}

	self, err := c.client.Agent().Self()
	if err != nil {
		return plan.Cluster{}, fmt.Errorf("read agent config from %s: %w", c.addr, err)
	}
	if dc, ok := self["Config"]["Datacenter"].(string); ok {
		cluster.Name = dc
	}

	return cluster, nil
}

// Health reads the datacenter's own verdict on itself and on each host.
//
// The same snapshot a survey is built on, without the name: it is read again
// between every step of a run, and what the cluster calls itself does not
// change while one is in progress.
func (c *Consul) Health(ctx context.Context) (plan.Cluster, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)

	// An unhealthy datacenter answers 429, and this client decodes that rather
	// than treating it as a failure -- unlike Nomad's, which needs the reply
	// recovered from the error.
	health, err := c.client.Operator().AutopilotServerHealth(q)
	if err != nil {
		return plan.Cluster{}, fmt.Errorf("read server health from %s: %w", c.addr, err)
	}

	members, err := c.client.Agent().Members(false)
	if err != nil {
		return plan.Cluster{}, fmt.Errorf("list members from %s: %w", c.addr, err)
	}

	return assemble(health, members, time.Now().UTC()), nil
}

// assemble turns the two reads into a snapshot. Separate from Survey and free
// of I/O, because the reconciling is the part worth testing and the two calls
// above are not.
func assemble(health *api.OperatorHealthReply, members []*api.AgentMember, at time.Time) plan.Cluster {
	cluster := plan.Cluster{SurveyedAt: at}
	if health == nil {
		return cluster
	}
	cluster.Tolerance = health.FailureTolerance
	cluster.Healthy = health.Healthy

	servers := make(map[string]struct{}, len(health.Servers))
	for _, s := range health.Servers {
		servers[s.Name] = struct{}{}
		cluster.Members = append(cluster.Members, plan.Member{
			ID:      s.ID,
			Name:    s.Name,
			Addr:    hostOf(s.Address),
			Kind:    plan.KindServer,
			Version: s.Version,
			Primary: s.Leader,
			Voter:   s.Voter,
			Status:  s.SerfStatus,
			Healthy: s.Healthy,
			// Serving, because a Consul server that is up is in service. There
			// is nothing here to schedule onto and so nothing to be eligible
			// for; the field carries "in service" for this tool.
			Eligible:    s.Healthy,
			StableSince: s.StableSince,
		})
	}

	// An agent on a host that runs a server is the same host. Recording it
	// again would have the run restart one raft member twice.
	for _, m := range members {
		if m == nil || m.Tags[roleTag] == serverRole {
			continue
		}
		if _, dual := servers[m.Name]; dual {
			continue
		}

		up := m.Status == alive
		cluster.Members = append(cluster.Members, plan.Member{
			Name:    m.Name,
			Addr:    m.Addr,
			Kind:    plan.KindClient,
			Version: buildVersion(m.Tags[buildTag]),
			Status:  serfStatus(m.Status),
			Healthy: up,
			// An agent carries no work, so "accepting work" is "in the gossip
			// pool": that is the whole of what being in service means here.
			Eligible: up,
		})
	}

	cluster.Sort()
	return cluster
}

// buildVersion takes the version out of a member's build tag, which carries
// the revision alongside it as "2.0.3:d0f2be93".
func buildVersion(build string) string {
	version, _, _ := strings.Cut(build, ":")
	return version
}

// serfStatus names a member's gossip state, which the library reports as an
// int. Recorded in the tool's own vocabulary rather than normalised, as the
// survey does everywhere else.
func serfStatus(status int) string {
	switch status {
	case alive:
		return "alive"
	case 2:
		return "leaving"
	case 3:
		return "left"
	case 4:
		return "failed"
	default:
		return "none"
	}
}

// hostOf drops a port when one is present. Autopilot reports a server's raft
// address with its port; the gossip pool reports an agent's without.
func hostOf(addr string) string {
	addr = strings.TrimSpace(addr)
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
