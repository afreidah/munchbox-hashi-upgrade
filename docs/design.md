# Rolling HashiCorp upgrades - design

## Context

Upgrading Nomad, Consul or Vault across a cluster has ordering constraints:

- Servers can run a newer version than clients. Clients cannot run a newer
  version than servers.
- Only one server can be down at a time, and only while the remaining servers
  maintain quorum.
- The leader should transfer leadership explicitly before it is restarted,
  rather than triggering an election by going down.

These rules are simple but easy to get wrong when done by hand, and a partly
completed upgrade is harder to recover from than either the old or new state.

Binaries are installed by configuration management. A version pin is stored in
a `versions` data bag, and a converge on each host installs the pinned version.
An upgrade therefore consists of: update the pin, converge hosts in the correct
order, and check cluster health between hosts.

This tool automates that procedure.

## Commands

`plan` queries the cluster and writes a run file. It makes no changes: it does
not touch the pin, timers or hosts. Every change is a task in the generated
file.

`run` executes a run file and records each task's result in it. Running the
same file again resumes from where it stopped.

`status` reads a run file and prints its state. It needs no cluster access or
credentials and makes no changes, so an operator can inspect a stopped run
without having permissions to modify the cluster.

Because planning and execution are separate, the run file can be reviewed and
kept before anything changes, and the reviewed file is exactly what executes.

![The plan command surveys a cluster and writes a run file. The run command drives that file: the runner owns order and persistence, a table maps each task's command to a step, and steps reach the fleet through six interfaces. One client per tool answers the cluster questions, chosen by clients.For; a configuration-server client moves the pin and the timers; an ssh client runs the converge on each host. The status command reads the file and nothing else.](assets/architecture.svg)

## Key decisions

**Topology.** Read from each tool's API at plan time. A host that is both server
and client is recorded once, as a server. It runs one binary and one service,
so recording it under both roles would restart it twice and reduce the
cluster's fault tolerance twice for the same host.

**Client selection.** Each tool's client implements three operations: list
members, report current state, and transfer leadership away from a host.
`clients.For` returns the client for a tool. Code above that layer uses
interfaces, so adding a tool means adding a package and a case in
`clients.For`.

Operations that only some tools support are not on the shared interface. Only
Nomad schedules workloads, so a client that supports draining implements
`execute.Drainer`, and callers type-assert for it where needed. `Spec.Drain` is
removed at plan time for tools that do not schedule workloads.

**Voter detection.** Whether a cluster's members vote in a raft quorum depends
on the deployment, not the tool. Vault with Consul storage has no voters; Vault
with integrated raft storage does. `plan.Cluster.Votes` determines this from
the member list, so a cluster that changes storage backend is handled without
configuration.

**The run file.** The task list and survey results are written once. Each task
has a separate result record that is updated as the run proceeds. The file
records both the planned tasks and how far execution got.

**Gate conditions.** A converge exiting zero does not mean the host has
rejoined. Each gate polls the cluster until it reports the host at the target
version, healthy and in service. On timeout, the error includes the unmet
condition and the timeout duration.

The default timeout is ten minutes, polling every five seconds. A converge
downloads a few hundred megabytes, restarts a service and rejoins a cluster. A
shorter timeout fails runs that would have succeeded and leaves the operator
unsure whether the host is broken or just slow.

**Converge exit status.** `Converge` returns a non-zero exit code as a result,
not an error, because the command did run on the host. The caller checks the
exit code and stops the run at the failed converge, instead of proceeding to
the gate and waiting on a host where nothing was installed.

**Confirmation.** The run file records which tasks require operator
confirmation, so they can be reviewed ahead of time. There are two levels:

- A yes/no prompt, for tasks that warrant a pause.
- A typed confirmation, for leadership transfer and for restarting the former
  leader. The operator must type the host name, or the version if the task has
  no host.

**Downgrades.** Not supported. Downgrading a raft member is not reliably safe,
and the decision would come at the worst time. A failed run stops and reports
the failure. Compensations undo orchestration changes in reverse order
(converge timers, drained hosts). Installed versions are not changed.

**The version pin.** Set once and never reverted, regardless of the run's
outcome. Converge timers are stopped for the whole run, so nothing reads the pin
until the run ends. Reverting it causes two problems:

- Hosts already upgraded would converge back to the old version once the
  timers restart.
- On resume, the pin step is recorded as succeeded and is skipped, so every
  remaining host installs the old version and its gate waits for a version that
  will never be installed.

**Step boundaries.** The runner handles ordering, persistence and failure
handling. A step receives one task and returns its result. A table maps each
command to its implementation; this table is the only place a new command needs
to be registered.

## The sequence

```
survey    freeze-converge         stop the scheduled converges fleet-wide
          set-version-pin         move the pin every converge will read

servers   upgrade-member          each non-coordinating host in turn
          hand-off-coordination   move coordination off the last one
          upgrade-member          that host, last

clients   upgrade-member          each host that carries work, in turn

verify    verify-cluster          read the fleet back against the target
          thaw-converge           release the scheduled converges
```

![Four stages. Survey stops the scheduled converges and then moves the version pin. The server stage upgrades each non-coordinating host in turn, hands coordination off, then upgrades the host that had it. The client stage upgrades each host that carries work. Verify reads the fleet back and only then releases the converges. Every host is followed by a gate that polls the cluster until it reports the host back at the target version, healthy and in service.](assets/sequence.svg)

Reasons for this order:

- Timers are stopped **before** the pin changes. Otherwise a scheduled converge
  between the two steps could upgrade a host out of order and without a gate.
- The pin is changed **once**, before any host is upgraded. All hosts converge
  to the same target; they differ only in when they are converged.
- The leader is upgraded **last**, so leadership stays stable for most of the
  server stage.
- Leadership transfer is a **separate task** so it can be confirmed, retried
  and inspected on its own. The target is selected when the task runs, from
  current cluster state, not from the survey taken at plan time.
- Cluster verification runs **before** timers are restarted. Per-host gates
  only confirm the hosts in the run file. A host missing from the survey, or
  one whose converge did not pick up the new pin, would pass every gate and
  still run the old version.

The first task that makes a change the run cannot back out of is marked
irreversible. Before that task, stopping the run is harmless. After it, the
cluster is partly upgraded and completing the run is the safer option.

## Per-tool differences

The sequence is the same for all three tools. The differences below are all
contained in the tool's client package.

| | Nomad | Consul | Vault |
|---|---|---|---|
| servers from | autopilot health | autopilot health | `sys/ha-status` |
| clients from | node list | gossip pool, filtered on role tag | none |
| reconciled on | advertise address | name | name |
| version from | autopilot | member `build` tag | each node's `sys/health` |
| failure tolerance | autopilot | autopilot | derived |
| voters | yes | yes | no, with Consul storage |
| handoff destination | required | optional | not accepted |
| schedules work | yes | no | no |

**Nomad.** Servers and clients are read from two endpoints and matched by
advertise address, because the two endpoints format names differently and
neither lists every host. Nomad reports an unhealthy cluster as HTTP 429 with
the health response in the body. The Nomad API client treats any non-2xx as an
error and discards the body, so the health response is parsed out of the error.

**Consul.** Autopilot lists the servers. The gossip pool lists all agents,
including servers, so hosts already recorded as servers are skipped. A member's
version is parsed from its `build` tag, which also contains the git revision.
The Consul API client handles the 429 response itself.

**Vault.** Two reads. `sys/ha-status` returns the HA members as seen by the
active node. Each node is then queried directly at its own API address, because
a node that restarted sealed still appears in the HA set but serves no
requests, and only the node itself reports that. A sealed node is recorded as
unhealthy with a sealed status, which the gate reports instead of a generic
version mismatch. If a node does not respond, it is recorded using the active
node's view instead of failing the read, since upgrading a cluster with one
host down is a common case.

Vault with Consul storage has no raft state, so it has no voters and reports no
failure tolerance. Tolerance is calculated instead: the cluster serves requests
while at least one node is unsealed and active, so it can lose all but one
serving node. The two places that previously required a voter (the server
gate, and selecting the leadership transfer target) check `Cluster.Votes` first.

Vault step-down does not accept a target node. The target chosen by the step
is accepted and ignored, and the gate confirms that leadership moved.
Restarting a node seals it. Unsealing depends on the seal configuration; a
cluster without auto-unseal cannot be upgraded unattended.

## Run modes

| mode | behavior |
|---|---|
| live | all tasks execute |
| `--dry-run` | read-only tasks execute; other tasks print what they would do |
| `--no-op` | no connections are made; every task prints and completes |

A dry run verifies that the cluster is reachable. A no-op verifies that the file
parses and shows the task sequence. Neither marks tasks as succeeded: a task
that only printed is recorded as *unnecessary*. Neither mode writes the run
file, so a rehearsal does not affect a subsequent live run.

A no-op creates no clients. A dry run creates only the cluster client, so it
does not require ssh or configuration-server credentials it will not use.

## Outcomes

| outcome | meaning |
|---|---|
| waiting | not started |
| active | started, no result recorded |
| succeeded | completed |
| unnecessary | no action needed, e.g. host already at target version |
| failed | returned an error |

![Each task settles as succeeded or unnecessary, or does not settle at all. A resumed run starts at the first task that has not settled. Failed means the task reported an error; active means it started and never reported back, which is what an interrupt leaves behind. The status command says which task and why, and run with the reset flag forgets that task's record so the run reaches it again once an operator has looked at the host.](assets/resume.svg)

`active` is the state left by an interrupt. Whether the task's change was
applied is unknown, so a resumed run stops on it rather than retrying
automatically. `failed` also blocks resume; the run does not skip past a
failure.

Both states require an operator to check the host first. `status` shows the
task and the error. `run --reset <task-id>` clears that task's result so the
run executes it again. The reset is explicit by design: the tool does not
assume a partly completed converge is safe to repeat; the operator decides.

## Confirmation and interruption

Tasks that require confirmation prompt the operator before running. Declining
is not treated as a failure: the run executes its compensations and reports its
progress. End of input (EOF) is treated as declining, so a run reading from a
pipe cannot pass a typed confirmation by accident.

`--yes` accepts all prompts. It is intended for resuming a run that has already
been reviewed, not for a first run.

Interrupts are checked between tasks, not during a task, so the run always
stops at a point the run file can represent.

## Prerequisites

- **Cluster API access.** Nomad: read autopilot health, list nodes, read the
  `cluster/identity` variable, transfer raft leadership, update node drain
  state. Consul: read autopilot health, list members, read agent config,
  transfer raft leadership. Vault: read `sys/ha-status`, and `update` on
  `sys/step-down`.
- **A configuration server** with the `versions` data bag, and a client
  identity whose key signs requests. For an internal CA, `--cinc-ca` takes the
  PEM file or directory to trust.
- **ssh access to every host** with CA-signed host keys. The tool requires a
  host CA and rejects unsigned host keys; a host with an unsigned key fails at
  the first task that connects to it.
- **`cinc-client` and `cinc-client.timer` on each host**, used to stop, run and
  restart converges.
- **Auto-unseal**, for Vault only. Restarting a node seals it.

## Testing

Three test tiers, each covering what the others cannot.

**Unit.** No Docker required. The configuration server is `cinc-server-ng`
embedded as a library, so pins are written and read back through the Chef API
with Mixlib signature verification. This tests a full round trip rather than
only checking request contents. Clients are consumer-side interfaces with
generated mocks, so ordering, compensations and failure paths are tested without
a cluster. Each client package is also tested against an HTTP test server to
verify endpoints, per-node addressing and error wrapping.

**Integration.** Uses the `integration` build tag and testcontainers. Covers
behavior that only a real cluster exhibits: drain completion, whether the
endpoints read match the endpoints served, and whether a dual-role host
reconciles to one member. Nomad only.

**Local clusters for manual testing.** One Docker Compose project per tool,
each on its own configuration-server port. Nomad and Consul run three servers
and two clients. Vault runs three servers, plus a Consul cluster for storage
and a second Vault instance for transit auto-unseal in place of a cloud KMS.
Each cluster starts one version behind the target, because a cluster already
at the target produces a run where every task is a no-op.

All three use the same node image. A build argument selects which binary to
copy from the official image. The tool name is written to a file rather than
an environment variable, because converges run over ssh and ssh sessions do
not inherit the image's environment.

### Bugs found outside unit tests

These justify the cost of the integration and local-cluster tiers. None could
have been caught by unit tests, and three reached production.

Found with local clusters:

- Unhealthy clusters were never detected. Autopilot reports an unhealthy
  cluster as HTTP 429 with the health response in the body, and the API client
  discards the body of every non-2xx response. `Cluster.Healthy` always
  returned true, so gates using it checked nothing.
- The server gate waited on a stability timestamp that did not change. An agent
  that restarts within one health-check interval is never seen as unhealthy, so
  the gate never passed for a host that had already rejoined. The gate now uses
  the version reported by the running agent, not the pin, as evidence of the
  restart.
- A failed converge was treated as success. `Converge` returns a non-zero exit
  as a result rather than an error, and the caller ignored it. The run then
  waited at a gate for a host where nothing was installed, and reported a
  version mismatch.
- A voter was required in clusters with no voters. This affected two places:
  the server gate, which would have held every Vault host until timeout, and
  leadership transfer target selection, which would fail later, after two
  servers were upgraded and the run was past the irreversible point.

Found in production:

- A rehearsal wrote the run file, so the following live run found every task
  already complete, did nothing, and reported success. Three tests asserted
  the incorrect behavior.
- Stopping converges failed immediately if a converge was already running,
  requiring a manual wait and retry. It now waits for the running converge and
  reports what it is waiting on.
- A failed run reverted the pin, and resuming did not restore it because the
  pin step was recorded as succeeded. Three hosts converged to the old version.
- Gates timed out after three minutes, which is shorter than a converge,
  restart and rejoin take on a slow host.

## Out of scope

- **Downgrades.** See above.
- **Cookbook correctness.** The tool sets a pin and runs a converge. What the
  converge installs is the cookbook's responsibility.
- **Scheduling.** Runs are started manually. There is no daemon or cron job.
- **Agents that are not cluster members.** Vault's `vault-agent` is installed
  from apt on other hosts and does not use the pin. Runs do not upgrade or
  report on it.

## Open items

- The cluster barrier (cluster healthy, all servers in service, enough
  tolerance to lose another server) is implemented and tested but not called by
  any step. Per-host gates check the host a step changed; the barrier would
  check the whole cluster.
- Integration tests cover Nomad only. Consul and Vault are covered by unit
  tests against an HTTP test server and by manual local-cluster runs, so
  leadership transfer and the leader gate have no automated test against a
  real election.
- Draining a client before restarting it is implemented but disabled by
  default, because restarting a Nomad agent does not stop its allocations. It
  has not been used on a production cluster.
- If the survey finds only one server, the plan still includes a leadership
  transfer task, which will fail. This is intentional, so that a degraded
  cluster is not hidden from the operator, but the failure happens during the
  run instead of at review time.
- `scripts/preflight.sh` uses the current user's default ssh identity and does
  not accept a key, so it cannot check hosts that require different
  credentials.

## Safety summary

- `plan` and `status` make no changes.
- Converge timers are stopped before the pin changes, and restarted only after
  the cluster is verified.
- One host at a time. Servers before clients. Leader last.
- Every wait polls cluster state, with a timeout that reports the unmet
  condition.
- A failed converge stops the run at that host.
- The run file is written after every task, so an interrupted run resumes
  rather than restarts, and a failed task is not retried until an operator
  resets it.
- On failure, the run stops and undoes orchestration changes in reverse order.
  Versions and the pin are never reverted.
- A task with no registered implementation fails with its command name instead
  of being skipped, so a run cannot report success for work it did not do.
