# Rolling HashiCorp upgrades - design

## Context

Upgrading Nomad across a fleet is a sequence with rules. Servers tolerate
running ahead of clients and never the reverse. Only one server may be down at
a time, and only while the rest can still form a quorum. The host that
coordinates should give that up deliberately rather than by vanishing. None of
that is hard; all of it is easy to get wrong at two in the morning, and a
half-finished upgrade is worse than either end of it.

The fleet installs its binaries through configuration management: a version pin
in a `versions` data bag, and a converge on each host that installs whatever
the pin says. So an upgrade is not "push a binary" -- it is "move the pin, then
converge the hosts in the right order, checking the cluster between each".

This tool is that sequence, written down and driven.

## What it is

Three commands.

`plan` reads the cluster and writes a run file. It changes nothing: no pin, no
timer, no host. Everything that alters anything is a task *in* the file it
produces.

`run` carries out a run file, recording the outcome of every task back into it.
It resumes, so a run that stopped is continued by naming the same file.

`status` reads a run file and reports it. No cluster, no credentials, no
change: a run that stopped in the night is read before deciding what to do
about it, and reading it should not require the ability to act on it.

The separation is the point. A plan can be read, questioned and kept before
anything happens, and the thing that is reviewed is the thing that runs.

## Key decisions

**Topology is discovered, never declared.** Servers come from autopilot's
health view, clients from the node list, reconciled on advertise address --
the two reads disagree about name shape and do not both list every host. A
host that holds both roles is recorded as a server, because it carries one
binary and one service: counting it twice would restart one raft member twice
and spend the cluster's fault tolerance twice for one host.

**The run file is the journal, not a script.** Task list and survey are written
once and never change; outcomes accumulate against them in a separate record
per task. Reading the file therefore answers both what was intended and how far
it got, without one obscuring the other. A run is resumed by opening the file
and finding the first task that has not settled.

**Gates read the cluster, never the clock.** A host is not upgraded because its
converge exited zero. It is upgraded when the cluster says it is back at the
target version, healthy, and either voting again or accepting work again. Every
wait is a poll of the cluster's own verdict with a timeout, and a timeout
reports the condition it gave up on rather than only its duration.

**Confirmation is a property of the task, not a decision the runner makes.**
The file records which boundaries need a person, so which ones they are can be
reviewed in advance. Two strengths: a prompt for anything worth pausing on, and
a typed confirmation for the two that cannot be waved through -- moving
coordination, and restarting the host that had it. Typed asks for the host's
name back, or for the version on a task that names no host.

**No automatic downgrade.** Reversing a raft member is not obviously safe, and
a tool that tried would be making the worst decision at the worst time. A
failed run stops and reports. Compensations unwind *orchestration* state in
reverse -- the converge timers, a drained host, a pin the run itself moved --
and never a version. A host running the new binary is running it.

**Steps know nothing about order.** The runner owns sequence, persistence and
what a failure costs. A step is handed one task and reports what it did. The
two are joined by a table mapping each command to its implementation, which is
the one place a new command is wired up.

## The sequence

```
survey    freeze-converge         stop the scheduled converges fleet-wide
          set-version-pin         move the pin every converge will read

servers   upgrade-member          each non-leader in turn
          hand-off-coordination   move coordination off the leader
          upgrade-member          the former leader, last

clients   upgrade-member          each host that carries work, in turn

verify    verify-cluster          read the fleet back against the target
          thaw-converge           release the scheduled converges
```

Why in that order:

- The timers stop **before** the pin moves. A scheduled converge landing
  between the two would install the new version on a host the run has not
  reached, out of order and unobserved.
- The pin moves **once, centrally**, before any host is touched, so hosts
  differ only in when they are converged and never in what they converge
  toward.
- The leader goes **last**, so the cluster spends most of the server stage with
  settled coordination.
- The handoff is a **separate task** so it can be confirmed, retried and read on
  its own. It names no destination: the runner picks a healthy voter when it
  gets there, from a cluster it has just read, rather than from a survey taken
  before anything was touched.
- Verification runs **before** the timers come back. The per-host gates each
  proved one host returned, which is not the same as the fleet having arrived:
  a host the survey missed, or one whose converge was a no-op because the pin
  never reached it, passes every gate and still runs the old binary.

The first task that changes the cluster in a way the run cannot walk away from
is marked irreversible. Before it, abandoning costs nothing. After it, the
fleet is part-upgraded and finishing is the safe direction.

## Run modes

| mode | what happens |
|---|---|
| live | every task is carried out |
| `--dry-run` | the read-only tasks run for real; the rest print what they would do |
| `--no-op` | nothing is reached at all; every task prints and settles |

A dry run proves the cluster answers. A no-op proves the file parses and shows
the sequence. Neither claims success: a task that only printed settles as
*unnecessary*, so a rehearsal advances through the file without the record
saying work was done.

A no-op builds no clients, and a dry run builds only the cluster client, since
demanding ssh and configuration-server credentials for a rehearsal that will
not use them would put it behind a wall of flags.

## Outcomes

| outcome | meaning |
|---|---|
| waiting | not reached |
| active | started and never reported back |
| succeeded | done |
| unnecessary | did not need doing -- a host already at the target |
| failed | reported an error |

`active` is what an interrupt leaves behind: whether the task took effect is
unknown, so a resumed run lands on it rather than retrying blind. `failed` is
deliberately not settled either -- a resumed run stops on the failure instead
of stepping over it.

Both want an operator looking at the host first. `status` says which task and
why; `run --reset <task-id>` then forgets that task's record so the run reaches
it again. Naming it is the point: the run will not decide for itself that a
half-done converge is safe to repeat, and the operator saying so explicitly is
the difference between a retry and a guess.

## Confirmation and interruption

A gated task is put to the operator before it runs. Declining is not a failure:
the operator has decided to stop, so the run unwinds its compensations and
reports where it got to. End of input counts as declining -- a run driven from
a pipe reaches a prompt it cannot ask, and treating silence as assent would let
it walk through a typed confirmation.

`--yes` assents to everything. That is for a run already reviewed and
restarted, not for the first pass over one.

Interrupts are checked between tasks rather than mid-task, so a run stops at a
boundary the file can describe.

## Prerequisites

- **Cluster API access** with a token that can read autopilot health, list
  nodes, read the `cluster/identity` variable, transfer raft leadership, and
  update node drain state.
- **A configuration server** holding the `versions` data bag, reachable with an
  identity whose key signs requests.
- **ssh to every host**, with a certificate-verified host key: the tool takes a
  host CA and will not accept a bare host key.
- **`cinc-client` and `cinc-client.timer` on each host**, since freezing,
  converging and thawing are what the tool drives.

## Testing

Three tiers, each for what only it can prove.

**Unit.** No Docker. The configuration server is `cinc-server-ng` embedded as a
library, so pins are written and read back through the real Chef API with real
Mixlib signature verification -- a round trip, rather than a table of expected
requests that would pass even if nothing landed. Clients are taken as
consumer-side interfaces with generated mocks, so ordering, compensations and
failure paths are exercised without a cluster.

**Integration.** Behind the `integration` build tag, Nomad in containers via
testcontainers. Some questions are properties of a cluster and nothing else
settles them: whether a drain has actually finished, whether the endpoints read
are the ones Nomad serves, whether a dual-role host reconciles to one member.

**A fleet to drive by hand.** `make cluster-up` starts three servers, two
clients and a configuration server, deliberately behind the version you plan
toward. The hosts carry stand-ins for the two things a container has not got --
a `systemctl` answering the timer commands and a `cinc-client` that reads the
pin and installs it. What that proves is the orchestration around a converge,
not that a cookbook installs Nomad; the cookbook lives elsewhere and is its own
concern.

That third tier earned its cost immediately. Three faults it found, none of
which a unit test could have:

- An unhealthy cluster was unreachable. Autopilot reports one as HTTP 429
  carrying the health reply, and the API client treats every non-2xx as an
  error and discards the body -- so `Cluster.Healthy` could only ever be true,
  and every gate reading it was checking nothing.
- The server gate waited on a stability timestamp that does not move. An agent
  that goes down and returns inside one health interval is never observed
  unhealthy, so the gate could not pass on a host that had already arrived. The
  version proves the restart on its own: it is read from the running agent, not
  from the pin.
- The pin could not be put back when the run had created it. Restoring an absent
  pin means removing the item, not writing an empty version -- and leaving it
  set would have had every host converge to it the moment the unwind released
  the timers.

## Out of scope

- **Consul and Vault.** Refused by name rather than attempted. The shape is
  meant to carry them, but each has its own notion of what "back" means and
  neither has been written.
- **Downgrades.** See above.
- **Cookbook correctness.** The tool moves a pin and runs a converge. What the
  converge installs is the cookbook's business.
- **Scheduling.** A run is started by a person. There is no daemon and no cron.

## Open items

- The cluster barrier -- healthy, every server a voter, tolerance to lose
  another -- is written and tested but no step calls it. The per-host gates
  cover what a step did to the host it touched; the barrier is what it did to
  the cluster.
- Draining a client before restarting it is implemented and off by default,
  since restarting an agent does not stop its allocations. It has never been
  driven across a real fleet.
- A survey that finds a single server still emits a handoff task, which cannot
  succeed. That is deliberate -- planning around it would hide a degraded
  cluster from the operator -- but it means the condition surfaces mid-run
  rather than at review.

## Safety summary

- Nothing is changed by `plan`.
- The timers are stopped before the pin moves, and released only after the fleet
  has been verified.
- One host at a time. Servers before clients. The coordinating host last.
- Every wait is on the cluster's own verdict, with a timeout that names the
  condition it gave up on.
- The file is written after every task, so an interrupted run is resumed rather
  than restarted, and a task that failed is not stepped over without a person
  naming it.
- A failure stops the run and unwinds orchestration state in reverse. Versions
  are never reversed.
- A task whose command has no implementation is refused by name rather than
  stepped over, so a run cannot report success over work it skipped.
