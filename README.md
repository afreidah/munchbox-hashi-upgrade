<p align="center">
  <img src="logo.png" alt="Munchbox" width="160">
</p>

# munchbox-hashi-upgrade

Rolling upgrades for Nomad, Consul and Vault.

`plan` queries the cluster API for each host's roles and writes a run file. The
run file lists the tasks in order and the health gates between them. `run`
executes the run file one host at a time and rewrites it after each task, so an
interrupted run resumes when you pass the same file to `run` again.

```
hashi-upgrade plan consul --to 2.0.4     # query the cluster and write a run file
hashi-upgrade run consul-*.yaml          # execute the run file
hashi-upgrade status consul-*.yaml       # show task progress
```

`plan` makes no changes. `status` reads only the run file and needs no cluster
access or credentials. `run` is the only command that makes changes.

## Status

Used in production for all three tools:

| tool | hosts | from | to |
|---|---|---|---|
| Nomad | 12 | 2.0.5 | 2.0.7 |
| Consul | 17 | 2.0.3 | 2.0.4 |
| Vault | 3 | 2.0.4 | 2.1.1 |

CI runs the unit tests. The integration tests and local clusters require Docker
and are run manually.

Known gaps:

- The cluster barrier is implemented but not called by any step.
- Draining a client before restart is implemented but disabled by default, and
  has not been used on a production cluster.
- Integration tests cover Nomad only.

## Run stages

```
survey    stop the scheduled converges, then move the version pin
servers   every non-coordinating host in turn, then hand off, then the last one
clients   every host that carries work, in turn
verify    read the cluster back, then release the converges
```

![Four stages. Survey stops the scheduled converges and then moves the version pin. The server stage upgrades each non-coordinating host in turn, hands coordination off, then upgrades the host that had it. The client stage upgrades each host that carries work. Verify reads the fleet back and only then releases the converges. Every host is followed by a gate that polls the cluster until it reports the host back at the target version, healthy and in service.](docs/assets/sequence.svg)

Hosts are upgraded one at a time: servers first, then clients. The server that
currently holds leadership is upgraded last, after leadership is transferred.
After each host, the tool polls the cluster until the host reports the target
version, passes health checks, and is back in service.

Tools without clients skip the client stage. For Vault, only the servers are
upgraded; `vault-agent` on other hosts is installed from apt and is not
controlled by the version pin.

## Design

- **Topology comes from the cluster API.** Hosts and roles are not configured
  in this repository. A host that is both server and client is recorded once,
  as a server, because it runs a single binary and service. Upgrading it once
  per role would restart it twice.
- **Servers before clients, one host at a time.** Clusters support servers
  running a newer version than clients, but not the reverse.
- **The run file holds all run state.** It includes the survey results, task
  list and task status, so a run can be resumed, audited or handed to another
  operator.
- **Gates poll cluster state, not timers.** The next host starts only after the
  cluster reports the previous one healthy. On timeout, the error names the
  condition that was not met.
- **Confirmation prompts are recorded per task.** The run file marks which
  tasks require operator confirmation, so they can be reviewed before running.
- **Tool-specific logic is isolated in one client package per tool.** The
  runner, steps and gates use interfaces. Adding a tool means adding a package
  and a case in `clients.For`.
- **No automatic rollback.** Downgrading a raft member is not reliably safe, so
  a failed run stops and reports the failure. Cleanup restores orchestration
  state only (converge timers, drained hosts). It does not revert versions or
  the version pin.

## Repository rule

Do not commit cluster-specific data: no hostnames, addresses, node names or
host counts. Topology is read at runtime, so none of it is needed here. Tests
use fixtures and containers, not real inventory.

## Local clusters

Each tool has its own Docker Compose project with its own configuration-server
port, so multiple clusters can run at the same time.

```
TOOL=consul make cluster-up      # start a cluster at FROM_VERSION
TOOL=consul make cluster-plan    # write a run file for it
TOOL=consul make cluster-run     # execute the run file; ARGS=--yes skips prompts
TOOL=consul make cluster-down    # stop it and delete its state
```

`TOOL` is `nomad` (default), `consul` or `vault`. `FROM_VERSION` and `TO`
override the default versions, which are one release apart so that the test
performs an actual upgrade.

Containers have no systemd or cinc-client, so each host has stubs for both: a
`systemctl` that handles the timer commands, and a `cinc-client` that reads the
version pin and installs that version. This tests the orchestration around a
converge. It does not test the cookbook that installs the binary.

The Vault environment also runs:

- A Consul cluster as the Vault storage backend, for deployments that use
  Consul storage rather than integrated raft.
- A second Vault instance providing a transit key for auto-unseal, in place of
  a cloud KMS, so restarted nodes unseal without manual input.

## Before a live run

```
./scripts/preflight.sh <run-file>
```

Connects to every host in the run file and checks that `cinc-client` is
installed and its timer is running. A dry run does not create ssh connections,
so without this check ssh problems would first appear mid-run, after the
version pin has changed.

The script uses the current user's default ssh identity. It verifies that
hosts are reachable, not that the credentials `run` will use are valid.

## Resuming

`run` resumes at the first task that has not completed. It will not skip a
task that failed or did not report a result, because it cannot know whether
that task's change was applied.

```
hashi-upgrade status <run-file>                      # show task results
hashi-upgrade run <run-file> --reset <task-id>       # mark the task pending and resume
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

Unit tests need no external services. They embed [cinc-server-ng] as a library
for the configuration server, so version pins are written and read through the
Chef API with signature verification. Integration tests use the `integration`
build tag and start containers.

See [docs/design.md](docs/design.md) for design rationale and per-tool
differences.

[cinc-server-ng]: https://github.com/cinc-project/cinc-server-ng

## License

MIT. See [LICENSE](LICENSE).
