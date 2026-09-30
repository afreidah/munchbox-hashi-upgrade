<p align="center">
  <img src="logo.png" alt="Munchbox" width="160">
</p>

# munchbox-hashi-upgrade

Rolling upgrades for Nomad, Consul and Vault.

The tool surveys a cluster through its own API, writes a run file whose task
order and gates are derived from what each host actually carries, and then
drives that file host by host. The file is the journal: it is rewritten after
every task, so a run that stops for any reason is resumed by naming it again.

```
hashi-upgrade plan nomad --to 2.0.7     # survey, and write a run file
hashi-upgrade run nomad-*.yaml          # carry it out
hashi-upgrade status nomad-*.yaml       # read where it got to
```

`run` resumes: it starts from the first task that has not settled, so a run
that stopped is continued by naming the same file again. It will not step over
a task that failed or never reported back -- whether that one took effect is
exactly what is unknown -- so `status` says what happened and
`run --reset <task-id>` puts it back into play once the host has been looked
at.

See [docs/design.md](docs/design.md) for why it is shaped this way.

## Status

Working end to end for Nomad. A five-host fleet -- three servers, two clients --
has been rolled from 2.0.5 to 2.0.7 by hand against the local test cluster,
gates, handoff and all. It has not yet been run against a production fleet.

CI runs the unit tier. The integration tier and the local fleet are run on
demand, because both need Docker.

Not done: Consul and Vault are refused by name rather than attempted, the
cluster barrier between hosts is written but not wired into a step, and
draining a client before restarting it is implemented but has never been driven
across a real fleet.

## What a run does

```
survey    stop the scheduled converges, then move the version pin
servers   every non-leader in turn, then hand off coordination, then the leader
clients   every host that carries work, in turn
verify    read the cluster back, then release the converges
```

One host at a time, servers before clients, and the coordinating host last.
Between each, the cluster is read until it says the host is back: running the
target version, healthy, and either voting again or accepting work again.

## Design

- **Topology is discovered, never declared.** Servers come from autopilot and
  clients from the node list, reconciled on advertise address. A host that holds
  both roles is a server: it carries one binary and one service, so it is
  upgraded once, with the servers.
- **Servers before clients, one at a time.** A cluster tolerates its servers
  running ahead of its clients and not the reverse. Non-leaders go first;
  coordination is handed over deliberately and the leader is upgraded last.
- **The run file is the contract.** Everything needed to resume, audit or hand
  off an upgrade in flight lives in it, including the survey it was built from.
- **Gates read the cluster, not the clock.** A host is upgraded when the cluster
  says it is back, never after a sleep.
- **Confirmation is a property of the task.** The file records which boundaries
  need a person, so it can be reviewed before anything runs.
- **No automatic downgrade.** Reversing a raft member is not obviously safe, so
  a failed run stops and reports. Compensations restore orchestration state --
  the converge timers, a drained host, a pin the run itself moved -- never a
  version.

## Repository rule

**No cluster specifics belong in this repository.** No hostnames, no addresses,
no node names, no counts. Topology is discovered at runtime, so a literal here
is both unnecessary and a leak. Tests use fixtures and containers, never a real
inventory.

## Running it against something first

A local fleet, deliberately behind the version you plan toward:

```
make cluster-up      # 3 servers, 2 clients, a configuration server
make cluster-plan    # survey it and write a run file
make cluster-run     # drive the file; ARGS=--yes to skip confirmations
make cluster-down    # stop it and discard its state
```

The hosts carry stand-ins for the two things a container has not got: a
`systemctl` that answers the timer commands, and a `cinc-client` that reads the
pin and installs it. What that proves is the orchestration around a converge.
Whether a cookbook installs Nomad correctly belongs to the cookbook.

## Development

```
make help                 # list targets
make check                # fast local loop: tests + vet
make test                 # race detector and coverage
make coverage             # write coverage.out
make integration-test     # against Nomad in containers; needs Docker
make integration-coverage # write integration-coverage.out
make lint                 # golangci-lint
make govulncheck          # dependency vulnerability scan
make generate             # regenerate mocks
```

Tests come in two tiers. The unit tier needs nothing running: the
configuration server is [cinc-server-ng] embedded as a library, so pins are
written and read back through the real Chef API with real signature
verification. The integration tier is behind the `integration` build tag and
starts Nomad in containers, because whether a drain has finished is a property
of a cluster and only a cluster settles it.

[cinc-server-ng]: https://github.com/cinc-project/cinc-server-ng

## License

MIT. See [LICENSE](LICENSE).
