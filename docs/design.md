# Rolling HashiCorp upgrades - design

## Context

Upgrading Nomad, Consul or Vault across a fleet is a sequence with rules.
Servers tolerate running ahead of clients and never the reverse. Only one
server may be down at a time, and only while the rest can still coordinate. The
host that coordinates should give that up deliberately rather than by
vanishing. None of that is hard; all of it is easy to get wrong at two in the
morning, and a half-finished upgrade is worse than either end of it.

The fleet installs its binaries through configuration management: a version pin
in a `versions` data bag, and a converge on each host that installs whatever the
pin says. An upgrade is therefore not "push a binary" -- it is "move the pin,
then converge the hosts in the right order, checking the cluster between each".

This tool is that sequence, written down and driven.

## What it is

Three commands.

`plan` reads the cluster and writes a run file. It changes nothing: no pin, no
timer, no host. Everything that alters anything is a task *in* the file it
produces.

`run` carries out a run file, recording the outcome of every task back into it.
It resumes, so a run that stopped is continued by naming the same file.

`status` reads a run file and reports it. No cluster, no credentials, no change:
a run that stopped in the night is read before deciding what to do about it, and
reading it should not require the ability to act on it.

The separation is the point. A plan can be read, questioned and kept before
anything happens, and the thing that is reviewed is the thing that runs.

![The plan command surveys a cluster and writes a run file. The run command drives that file: the runner owns order and persistence, a table maps each task's command to a step, and steps reach the fleet through six interfaces. One client per tool answers the cluster questions, chosen by clients.For; a configuration-server client moves the pin and the timers; an ssh client runs the converge on each host. The status command reads the file and nothing else.](assets/architecture.svg)

## Key decisions

**Topology is discovered, never declared.** Each tool is surveyed through its
own API. A host that holds two roles is recorded as a server, because it carries
one binary and one service: counting it twice would restart one member twice and
spend the cluster's fault tolerance twice for one host.

**One client per tool, behind one interface.** A cluster answers three
questions: what it is made of, how it is now, and move coordination off this
host. `clients.For` is the single place a tool becomes a client; everything
above it takes interfaces and never learns which it holds. Adding a tool is a
package and a case, not a change to the runner, the steps or the gates.

Capabilities not every tool has stay off that interface. Only Nomad places work,
so a client that can drain says so by implementing `execute.Drainer` and is
asked at the point it would be used. `Spec.Drain` is dropped at plan time for a
tool that schedules nothing, rather than recorded and ignored.

**Cluster properties are read, not configured.** Whether coordination runs on a
quorum of the fleet's own members is a property of how a cluster is deployed,
not of which tool it is: Vault storing its data in Consul has no voters, and the
same Vault on integrated raft storage does. `plan.Cluster.Votes` answers it from
the members, so neither case is configured and a cluster migrated between them
is read correctly without being told.

**The run file is the journal, not a script.** Task list and survey are written
once and never change; outcomes accumulate against them in a separate record per
task. Reading the file answers both what was intended and how far it got,
without one obscuring the other.

**Gates read the cluster, never the clock.** A host is not upgraded because its
converge exited zero. It is upgraded when the cluster says it is back at the
target version, healthy, and in service. Every wait polls the cluster's own
verdict with a timeout, and a timeout reports the condition it gave up on rather
than only its duration.

The default is ten minutes at five-second intervals. A converge installs a
couple of hundred megabytes, restarts a service and rejoins a cluster; a gate
that gives up inside that fails a run that was going to succeed, and leaves the
operator to work out whether the host is wrong or merely unhurried.

**A converge that ran and failed is a result, not an error.** The command
reached the host and reported. Reading its exit status is what stops the run at
the failure instead of at the next gate, waiting for a host nothing installed
anything on.

**Confirmation is a property of the task, not a decision the runner makes.** The
file records which boundaries need a person, so which ones they are can be
reviewed in advance. Two strengths: a prompt for anything worth pausing on, and
a typed confirmation for the two that cannot be waved through -- moving
coordination, and restarting the host that had it. Typed asks for the host's
name back, or for the version on a task that names no host.

**No automatic downgrade.** Reversing a raft member is not obviously safe, and a
tool that tried would be making the worst decision at the worst time. A failed
run stops and reports. Compensations unwind *orchestration* state in reverse --
the converge timers, a drained host -- and never a version.

**The pin is intent, not a reversible side effect.** It stands whatever becomes
of the run. Converges are frozen throughout, so a pin nothing reads harms
nothing, and rolling it back does harm twice over: a fleet half-converged onto
the new version and aimed at the old one converges backwards the moment the
timers return, and a resume skips the step as already done, so every host after
it installs the old version and waits at a gate for a version no longer coming.

**Steps know nothing about order.** The runner owns sequence, persistence and
what a failure costs. A step is handed one task and reports what it did. The two
are joined by a table mapping each command to its implementation, which is the
one place a new command is wired up.

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

Why in that order:

- The timers stop **before** the pin moves. A scheduled converge landing between
  the two would install the new version on a host the run has not reached, out
  of order and unobserved.
- The pin moves **once, centrally**, before any host is touched, so hosts differ
  only in when they are converged and never in what they converge toward.
- The coordinating host goes **last**, so the cluster spends most of the server
  stage with settled coordination.
- The handoff is a **separate task** so it can be confirmed, retried and read on
  its own. The destination is chosen when the task runs, from a cluster just
  read, rather than from a survey taken before anything was touched.
- Verification runs **before** the timers come back. The per-host gates each
  proved one host returned, which is not the same as the fleet having arrived: a
  host the survey missed, or one whose converge was a no-op because the pin never
  reached it, passes every gate and still runs the old binary.

The first task that changes the cluster in a way the run cannot walk away from
is marked irreversible. Before it, abandoning costs nothing. After it, the fleet
is part-upgraded and finishing is the safe direction.

## What each tool does differently

The sequence above is the same for all three. These are the differences, and all
of them live in one client each.

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

**Nomad.** Two reads, reconciled on advertise address because the server and
client views disagree about name shape and do not both list every host. An
unhealthy cluster is reported as HTTP 429 carrying the health reply, and the API
client treats every non-2xx as an error and discards the body, so the reply is
recovered from the error rather than taken from the response.

**Consul.** Autopilot describes the servers; the gossip pool describes every
agent, servers included, so a host already recorded as a server is not recorded
again as an agent. A member's version comes out of its `build` tag, which
carries the revision alongside it. Consul's client decodes the 429 itself.

**Vault.** Two reads. `sys/ha-status` lists the HA set from the active node's
view. Then each node is asked about itself at its own API address, because a node
restarting into a seal is in the HA set and serving nothing, and only that node
knows -- a sealed node is recorded as unhealthy with a status that says so,
which the existing gate reports instead of a bare version mismatch. A node that
will not answer is recorded from the active node's view rather than failing the
read, since a cluster with one host down is a state a run is often called for.

Vault with Consul storage holds no raft state of its own, so nothing votes and
nothing reports a failure tolerance. Tolerance is derived: a cluster serves while
one node is unsealed and one is active, so what it can afford to lose is every
serving node but one. The two places that required a voter -- the server gate,
and choosing the host coordination is handed to -- ask `Cluster.Votes` first.

A step-down names no successor, so the one the step picked is accepted and
ignored and the gate confirms coordination moved. Restarting a node seals it;
whether it returns unsealed is the seal's business, and a cluster without
automatic unsealing cannot be rolled unattended.

## Run modes

| mode | what happens |
|---|---|
| live | every task is carried out |
| `--dry-run` | the read-only tasks run for real; the rest print what they would do |
| `--no-op` | nothing is reached at all; every task prints and settles |

A dry run proves the cluster answers. A no-op proves the file parses and shows
the sequence. Neither claims success: a task that only printed settles as
*unnecessary*, so a rehearsal advances through the file without the record
saying work was done. A rehearsal also does not write the file, so it cannot
consume the run a live pass is about to make.

A no-op builds no clients, and a dry run builds only the cluster client, since
demanding ssh and configuration-server credentials for a rehearsal that will not
use them would put it behind a wall of flags.

## Outcomes

| outcome | meaning |
|---|---|
| waiting | not reached |
| active | started and never reported back |
| succeeded | done |
| unnecessary | did not need doing -- a host already at the target |
| failed | reported an error |

![Each task settles as succeeded or unnecessary, or does not settle at all. A resumed run starts at the first task that has not settled. Failed means the task reported an error; active means it started and never reported back, which is what an interrupt leaves behind. The status command says which task and why, and run with the reset flag forgets that task's record so the run reaches it again once an operator has looked at the host.](assets/resume.svg)

`active` is what an interrupt leaves behind: whether the task took effect is
unknown, so a resumed run lands on it rather than retrying blind. `failed` is
deliberately not settled either -- a resumed run stops on the failure instead of
stepping over it.

Both want an operator looking at the host first. `status` says which task and
why; `run --reset <task-id>` then forgets that task's record so the run reaches
it again. Naming it is the point: the run will not decide for itself that a
half-done converge is safe to repeat, and the operator saying so explicitly is
the difference between a retry and a guess.

## Confirmation and interruption

A gated task is put to the operator before it runs. Declining is not a failure:
the operator has decided to stop, so the run unwinds its compensations and
reports where it got to. End of input counts as declining -- a run driven from a
pipe reaches a prompt it cannot ask, and treating silence as assent would let it
walk through a typed confirmation.

`--yes` assents to everything. That is for a run already reviewed and restarted,
not for the first pass over one.

Interrupts are checked between tasks rather than mid-task, so a run stops at a
boundary the file can describe.

## Prerequisites

- **Cluster API access.** Nomad: read autopilot health, list nodes, read the
  `cluster/identity` variable, transfer raft leadership, update node drain
  state. Consul: read autopilot health, list members, read agent config,
  transfer raft leadership. Vault: read `sys/ha-status`, and `update` on
  `sys/step-down`.
- **A configuration server** holding the `versions` data bag, reachable with an
  identity whose key signs requests. Behind an internal CA, `--cinc-ca` takes
  the PEM file or directory to trust.
- **ssh to every host**, with a certificate-verified host key: the tool takes a
  host CA and will not accept a bare host key. A host presenting an unsigned key
  is refused at the first task that dials it.
- **`cinc-client` and `cinc-client.timer` on each host**, since freezing,
  converging and thawing are what the tool drives.
- **Automatic unsealing**, for Vault only. A restart seals the node.

## Testing

Three tiers, each for what only it can prove.

**Unit.** No Docker. The configuration server is `cinc-server-ng` embedded as a
library, so pins are written and read back through the real Chef API with real
Mixlib signature verification -- a round trip, rather than a table of expected
requests that would pass even if nothing landed. Clients are taken as
consumer-side interfaces with generated mocks, so ordering, compensations and
failure paths are exercised without a cluster. Each client package is also
driven against an HTTP stand-in, which is what proves the endpoints, the
per-node addressing and the error wrapping are as issued.

**Integration.** Behind the `integration` build tag, containers via
testcontainers. Some questions are properties of a cluster and nothing else
settles them: whether a drain has finished, whether the endpoints read are the
ones served, whether a dual-role host reconciles to one member. Nomad only.

**A fleet to drive by hand.** One compose environment per tool, each a separate
project on its own configuration-server port. Three servers and two clients for
Nomad and Consul; three servers for Vault, plus the Consul it stores in and a
second Vault standing in for a cloud KMS. Every fleet starts behind the version
to plan toward, since a fleet already at the target plans a run whose every task
is a no-op.

One node image serves all three: a build argument picks which binary is lifted
out of the official image, and the tool's name is written to a file rather than
the environment, because a converge arrives over ssh and an ssh session carries
nothing the image set.

### Faults found by driving it

Listed because they are the argument for the cost of the last two tiers. None
could have been found by a unit test, and three reached production.

Found by a local fleet:

- An unhealthy cluster was unreachable. Autopilot reports one as HTTP 429
  carrying the health reply, and the API client discards the body of every
  non-2xx -- so `Cluster.Healthy` could only ever be true, and every gate
  reading it was checking nothing.
- The server gate waited on a stability timestamp that does not move. An agent
  that goes down and returns inside one health interval is never observed
  unhealthy, so the gate could not pass on a host that had already arrived. The
  version proves the restart on its own, read from the running agent rather than
  from the pin.
- A failed converge was taken as success. `Converge` returns a non-zero exit as a
  result rather than an error, and the caller discarded it, so the run walked
  into a gate waiting for a host nothing had installed anything on and then
  blamed the version.
- A voter was required of a host in a cluster where nothing votes. Two sites: the
  server gate, which would have held every Vault host until it timed out, and
  choosing a successor, which fails later and worse -- after two servers are
  upgraded and past the point of no return.

Found in production:

- A rehearsal consumed the run file, so the live pass that followed did nothing
  and reported success. Three tests had encoded that behaviour.
- Freezing refused outright when a converge was already in flight, which left
  nothing to do but wait and retry by hand. It now waits for the converge and
  reports what it is waiting on.
- The pin was rolled back by a failed run, and a resume could not put it back,
  because the step was recorded as succeeded. Three hosts converged onto the
  version the fleet was being upgraded away from.
- Gates gave up after three minutes, inside what a converge plus a restart plus
  a rejoin takes on a slow host.

## Out of scope

- **Downgrades.** See above.
- **Cookbook correctness.** The tool moves a pin and runs a converge. What the
  converge installs is the cookbook's business.
- **Scheduling.** A run is started by a person. There is no daemon and no cron.
- **Agents that are not fleet members.** Vault's `vault-agent` installs from apt
  on hosts across the fleet, independent of the pin. A run neither drives it nor
  reports on it.

## Open items

- The cluster barrier -- healthy, every server in service, tolerance to lose
  another -- is written and tested but no step calls it. The per-host gates
  cover what a step did to the host it touched; the barrier is what it did to
  the cluster.
- The integration tier covers Nomad only. Consul and Vault are covered by unit
  tests against an HTTP stand-in and by a fleet driven by hand, which leaves the
  handoff and the coordination gate without an automated test against a real
  election.
- Draining a client before restarting it is implemented and off by default,
  since restarting an agent does not stop its allocations. It has never been
  driven across a production fleet.
- A survey that finds a single server still emits a handoff task, which cannot
  succeed. That is deliberate -- planning around it would hide a degraded
  cluster from the operator -- but the condition surfaces mid-run rather than at
  review.
- `scripts/preflight.sh` authenticates as the invoking user's default ssh
  identity and takes no key, so it cannot check a fleet reachable only with
  credentials of its own.

## Safety summary

- Nothing is changed by `plan`, and nothing by `status`.
- The timers are stopped before the pin moves, and released only after the fleet
  has been verified.
- One host at a time. Servers before clients. The coordinating host last.
- Every wait is on the cluster's own verdict, with a timeout that names the
  condition it gave up on.
- A converge that reports failure stops the run where it failed.
- The file is written after every task, so an interrupted run is resumed rather
  than restarted, and a task that failed is not stepped over without a person
  naming it.
- A failure stops the run and unwinds orchestration state in reverse. Versions
  are never reversed, and neither is the pin.
- A task whose command has no implementation is refused by name rather than
  stepped over, so a run cannot report success over work it skipped.
