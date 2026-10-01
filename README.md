<p align="center">
  <img src="logo.png" alt="Munchbox" width="160">
</p>

# munchbox-hashi-upgrade

Rolling upgrades for Nomad, Consul and Vault.

The tool surveys a cluster through its own API, writes a run file whose task
order and gates are derived from what each host carries, and drives that file
host by host. The file is the journal: it is rewritten after every task, so a
run that stops for any reason is resumed by naming it again.

```
hashi-upgrade plan consul --to 2.0.4     # survey, and write a run file
hashi-upgrade run consul-*.yaml          # carry it out
hashi-upgrade status consul-*.yaml       # read where it got to
```

`plan` changes nothing. `status` needs no cluster and no credentials. Only `run`
alters anything.

## Status

Driven against production for all three tools:

| tool | hosts | from | to |
|---|---|---|---|
| Nomad | 12 | 2.0.5 | 2.0.7 |
| Consul | 17 | 2.0.3 | 2.0.4 |
| Vault | 3 | 2.0.4 | 2.1.1 |

CI runs the unit tier. The integration tier and the local fleets need Docker and
are run on demand.

Incomplete: the cluster barrier is written but no step calls it; draining a
client before restarting it is implemented, off by default, and has never been
driven across a production fleet; the integration tier covers Nomad only.

## What a run does

```
survey    stop the scheduled converges, then move the version pin
servers   every non-coordinating host in turn, then hand off, then the last one
clients   every host that carries work, in turn
verify    read the cluster back, then release the converges
```

![Four stages. Survey stops the scheduled converges and then moves the version pin. The server stage upgrades each non-coordinating host in turn, hands coordination off, then upgrades the host that had it. The client stage upgrades each host that carries work. Verify reads the fleet back and only then releases the converges. Every host is followed by a gate that polls the cluster until it reports the host back at the target version, healthy and in service.](docs/assets/sequence.svg)

One host at a time, servers before clients, the coordinating host last. Between
each, the cluster is read until it reports the host back: running the target
version, healthy, and in service.

A tool with no client stage has none. Vault's fleet is its servers; the
`vault-agent` on other hosts installs from apt and is not driven by the pin.

## Design

- **Topology is discovered, never declared.** Each tool is surveyed through its
  own API. A host holding two roles is recorded once, as a server: it carries
  one binary and one service, so upgrading it twice would spend the cluster's
  fault tolerance twice for one host.
- **Servers before clients, one at a time.** A cluster tolerates its servers
  running ahead of its clients and not the reverse.
- **The run file is the contract.** Everything needed to resume, audit or hand
  off an upgrade in flight is in it, including the survey it was built from.
- **Gates read the cluster, not the clock.** A host is upgraded when the cluster
  says it is back, never after a sleep. A timeout names the condition it gave up
  on.
- **Confirmation is a property of the task.** The file records which boundaries
  need a person, so they can be reviewed before anything runs.
- **Differences between tools live in one client each.** Everything above them
  takes interfaces: adding a tool is a package and a case in `clients.For`, not
  a change to the runner, the steps or the gates.
- **No automatic downgrade.** Reversing a raft member is not obviously safe, so
  a failed run stops and reports. Compensations restore orchestration state --
  the converge timers, a drained host -- never a version, and never the pin.

## Repository rule

**No cluster specifics belong in this repository.** No hostnames, no addresses,
no node names, no counts. Topology is discovered at runtime, so a literal here
is both unnecessary and a leak. Tests use fixtures and containers, never a real
inventory.

## Local fleets

One environment per tool, each a separate compose project on its own
configuration-server port, so more than one can run at once.

```
TOOL=consul make cluster-up      # start it, behind the version to plan toward
TOOL=consul make cluster-plan    # survey it and write a run file
TOOL=consul make cluster-run     # drive the file; ARGS=--yes to skip prompts
TOOL=consul make cluster-down    # stop it and discard its state
```

`TOOL` is `nomad`, `consul` or `vault`; `nomad` is the default. `FROM_VERSION`
and `TO` override the defaults, which sit one release apart so the environment
rehearses a real upgrade rather than a no-op.

Each host carries stand-ins for the two things a container has not got: a
`systemctl` that answers the timer commands, and a `cinc-client` that reads the
pin and installs it. What that proves is the orchestration around a converge.
Whether a cookbook installs the binary correctly belongs to the cookbook.

The Vault environment additionally runs the two services a Vault cluster needs
and does not contain: a Consul for storage, matching a fleet that stores there
rather than in raft of its own, and a second Vault providing a transit key in
place of a cloud KMS, so a restarted node returns unsealed unattended.

## Before a live run

```
./scripts/preflight.sh <run-file>
```

Reaches every host the run will dial and reports whether `cinc-client` is
present and its timer running. A dry run builds no ssh client, so that leg is
otherwise untested until a run is underway and the pin has moved.

The script authenticates as the invoking user's default ssh identity, so it
checks reachability rather than the exact credentials a run uses.

## Resuming

`run` starts from the first task that has not settled. It will not step over a
task that failed or never reported back -- whether that one took effect is
exactly what is unknown.

```
hashi-upgrade status <run-file>                      # what happened
hashi-upgrade run <run-file> --reset <task-id>       # put that task back in play
```

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

The unit tier needs nothing running: the configuration server is
[cinc-server-ng] embedded as a library, so pins are written and read back
through the real Chef API with real signature verification. The integration tier
is behind the `integration` build tag and starts containers.

See [docs/design.md](docs/design.md) for why it is shaped this way, and for what
each tool does differently.

[cinc-server-ng]: https://github.com/cinc-project/cinc-server-ng

## License

MIT. See [LICENSE](LICENSE).
