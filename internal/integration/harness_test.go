// -------------------------------------------------------------------------------
// Harness - A Nomad Cluster In Containers
//
// Author: Alex Freidah
//
// Two shapes, because the things worth testing need different clusters.
//
//	one      a dev agent: server and client in one process. Enough for every
//	         read, and for draining, since it registers a node of its own.
//	cluster  three servers on a network of their own. The only shape that can
//	         hold an election, which is what a leadership handoff is.
//
// The dev agent is started once for the package. A three-server cluster costs
// far more, so it is started only by the tests that need one.
//
// The image prints a warning that running Nomad clients in a container is
// unsupported, and it is right that this is not how to run one in production.
// It works here because of what vacateCgroupRoot does; what these tests need
// from a client is a registered node, not a task runner.
// -------------------------------------------------------------------------------

//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	nomadclient "github.com/afreidah/munchbox-hashi-upgrade/internal/clients/nomad"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// The Nomad the tests run against. A version the fleet has not reached yet is
// deliberate: the tool has to work against what the fleet is moving to, not
// only against what it is on.
const nomadImage = "hashicorp/nomad:2.0.7"

// The API port a Nomad agent serves on.
const apiPort = "4646/tcp"

// How long an agent is given to elect itself and answer. A dev agent is up in
// a second or two; the margin is for a cold image pull on a first run.
const startTimeout = 90 * time.Second

// Shell run before the agent, to get the container's own processes out of the
// cgroup root.
//
// Nomad's client half enables controllers by writing cgroup.subtree_control on
// the root, and cgroup v2 refuses that on any cgroup holding processes
// directly -- the "no internal processes" rule. The container's PID 1 is in
// that root, so the write fails with "device or resource busy" however the
// namespace is arranged. Moving it into a child leaves the root empty and the
// write legal.
const vacateCgroupRoot = `mkdir -p /sys/fs/cgroup/init && ` +
	`echo 1 > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null;`

// The two clusters the package shares, both set up by TestMain. A nil one is
// how a test learns Docker was unavailable and skips rather than failing.
// Why a cluster is missing is kept alongside it. go test discards what
// TestMain writes when the run passes, so a skip that only said "no fleet"
// would hide the reason a tier silently stopped covering anything.
var (
	one      *nomadclient.Nomad // dev agent: one host, both roles
	oneErr   error
	fleetAPI *nomadclient.Nomad // a server with a client of its own
	fleetErr error
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	var stop []func()

	// A dev agent binds loopback, which nothing outside the container reaches.
	dev, client, err := startAgent(ctx, agentSpec{args: []string{"-dev", "-bind=0.0.0.0"}})
	if oneErr = err; err == nil {
		one = client
		stop = append(stop, func() { _ = dev.Terminate(ctx) })
	}

	api, fleetStop, err := startFleet(ctx)
	stop = append(stop, fleetStop...)
	if fleetErr = err; err == nil {
		fleetAPI = api
	}

	code := m.Run()

	// Deliberately not deferred: os.Exit does not run deferred functions, and
	// a container left behind would outlive the test binary.
	for i := len(stop) - 1; i >= 0; i-- {
		stop[i]()
	}

	os.Exit(code)
}

// agentSpec is one agent to run: its arguments, and where it sits on a network
// so other agents can find it by name.
type agentSpec struct {
	args     []string
	network  string
	alias    string
	skipWait bool // a client elects nothing, so it has no leader to wait for
}

// waitStrategy is what start blocks on.
//
// /v1/status/leader rather than a log line or an open port: the port accepts
// connections before the agent has elected itself, and a read issued in that
// window fails for reasons that have nothing to do with the test. A client
// agent never reports a leader of its own, so it waits on the port instead and
// its registration is waited for through the server.
func (s agentSpec) waitStrategy() wait.Strategy {
	if s.skipWait {
		return wait.ForListeningPort(apiPort).WithStartupTimeout(startTimeout)
	}
	return wait.ForHTTP("/v1/status/leader").WithPort(apiPort).WithStartupTimeout(startTimeout)
}

// startAgent runs one Nomad agent and returns a client pointed at it.
func startAgent(ctx context.Context, spec agentSpec) (testcontainers.Container, *nomadclient.Nomad, error) {
	req := testcontainers.ContainerRequest{
		Image:        nomadImage,
		Entrypoint:   []string{"/bin/sh", "-c"},
		Cmd:          []string{vacateCgroupRoot + " exec nomad agent " + strings.Join(spec.args, " ")},
		ExposedPorts: []string{apiPort},
		WaitingFor:   spec.waitStrategy(),
		// Nomad's client half writes to cgroup.subtree_control, which fails
		// with "device or resource busy" in the host's cgroup namespace. Its
		// own namespace gives it a cgroup root it may write to, and without a
		// working client there is no node to drain.
		//
		// The cgroup filesystem itself must be the real one: mounting a tmpfs
		// over it satisfies the write but leaves cpuset.mems absent, which the
		// client also insists on.
		HostConfigModifier: func(cfg *container.HostConfig) {
			cfg.Privileged = true
			cfg.CgroupnsMode = container.CgroupnsModePrivate
			cfg.Binds = []string{"/var/run/docker.sock:/var/run/docker.sock"}
		},
	}

	if spec.network != "" {
		req.Networks = []string{spec.network}
		if spec.alias != "" {
			req.NetworkAliases = map[string][]string{spec.network: {spec.alias}}
		}
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start container: %w", err)
	}

	addr, err := apiAddress(ctx, container)
	if err != nil {
		return nil, nil, err
	}

	client, err := nomadclient.New(nomadclient.Options{Address: addr})
	if err != nil {
		return nil, nil, fmt.Errorf("build client for %s: %w", addr, err)
	}

	return container, client, nil
}

// apiAddress returns the URL the host reaches a container's API on. The mapped
// port is assigned at start, so it cannot be known before then.
func apiAddress(ctx context.Context, container testcontainers.Container) (string, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return "", fmt.Errorf("container host: %w", err)
	}

	port, err := container.MappedPort(ctx, apiPort)
	if err != nil {
		return "", fmt.Errorf("mapped api port: %w", err)
	}

	return fmt.Sprintf("http://%s:%s", host, port.Port()), nil
}

// requireAgent skips a test when the shared agent could not be started, so a
// machine without Docker reports skips rather than a wall of failures.
func requireAgent(t *testing.T) *nomadclient.Nomad {
	t.Helper()

	if one == nil {
		t.Skipf("no dev agent, so nothing here is covered: %v", oneErr)
	}
	return one
}

// -------------------------------------------------------------------------
// A SERVER AND A CLIENT OF ITS OWN
// -------------------------------------------------------------------------

// The name the client agent finds the server by, and the port it joins on.
const (
	serverAlias = "nomad-server"
	rpcPort     = "4647"
)

// startFleet runs a server and a separate client agent on a network of their
// own, and returns a client pointed at the server.
//
// A dev agent will not do for draining. The survey records a host holding both
// roles as a server, so what it carries is a raft id, and a drain needs a node
// id. Only a client that is its own host produces a member with one.
//
// Started once for the package: two containers and a gossip join cost ten
// seconds, where the state any one test needs costs nothing. The cleanups it
// returns are run by TestMain.
func startFleet(ctx context.Context) (*nomadclient.Nomad, []func(), error) {
	var stop []func()

	net, err := network.New(ctx)
	if err != nil {
		return nil, stop, fmt.Errorf("create network: %w", err)
	}
	stop = append(stop, func() { _ = net.Remove(ctx) })

	server, api, err := startAgent(ctx, agentSpec{
		args:    []string{"-server", "-bootstrap-expect=1", "-bind=0.0.0.0", "-data-dir=/tmp/nomad"},
		network: net.Name,
		alias:   serverAlias,
	})
	if err != nil {
		return nil, stop, fmt.Errorf("start server: %w", err)
	}
	stop = append(stop, func() { _ = server.Terminate(ctx) })

	worker, _, err := startAgent(ctx, agentSpec{
		args: []string{
			"-client", "-bind=0.0.0.0", "-data-dir=/tmp/nomad",
			"-servers=" + serverAlias + ":" + rpcPort,
		},
		network:  net.Name,
		skipWait: true,
	})
	if err != nil {
		return nil, stop, fmt.Errorf("start client: %w", err)
	}
	stop = append(stop, func() { _ = worker.Terminate(ctx) })

	if err := awaitClient(ctx, api); err != nil {
		return nil, stop, err
	}
	return api, stop, nil
}

// awaitClient blocks until the server holds a registered client node. Joining
// is gossip, so it finishes somewhat after the client's own API answers rather
// than at the same moment.
func awaitClient(ctx context.Context, api *nomadclient.Nomad) error {
	deadline := time.Now().Add(startTimeout)

	for time.Now().Before(deadline) {
		cluster, err := api.Health(ctx)
		if err == nil && len(cluster.OfKind(plan.KindClient)) > 0 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("no client node registered with the server")
}

// requireFleet skips a test when the shared server-and-client fleet could not
// be started, and returns the server's API with the client node to act on.
func requireFleet(t *testing.T) (*nomadclient.Nomad, plan.Member) {
	t.Helper()

	if fleetAPI == nil {
		t.Skipf("no nomad fleet, so nothing here is covered: %v", fleetErr)
	}

	cluster, err := fleetAPI.Health(t.Context())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	clients := cluster.OfKind(plan.KindClient)
	if len(clients) == 0 {
		t.Fatal("the server holds no client node")
	}
	return fleetAPI, clients[0]
}
