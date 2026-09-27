<p align="center">
  <img src="logo.png" alt="Munchbox" width="160">
</p>

# munchbox-hashi-upgrade

Rolling upgrades for Nomad, Consul and Vault.

The tool discovers cluster topology from the live API, generates a plan whose
step order and gates are derived from what each node actually carries, and then
drives `cinc-client` node by node against that plan. The plan file is the
journal: it is rewritten after every step, so an interrupted run resumes from
where it stopped rather than starting over.

## Status

Early. The scaffold is in place; the planner and runner are being built out.

## Design

- **Topology is discovered, never declared.** Servers come from the raft
  configuration and clients from the node list, joined on advertise address.
  Anything in the raft is treated as a server and upgraded once, in the server
  phase, whether or not it is also a client.
- **Servers before clients, one at a time.** Nomad supports servers ahead of
  clients and never the reverse. Non-leaders go first, ordered by how much they
  carry; leadership is transferred deliberately and the leader is upgraded last.
- **The plan is the contract.** Everything needed to resume, audit or hand off
  an in-flight upgrade lives in the plan file, including the topology snapshot
  it was generated from.
- **Gates are per step.** Routine steps run unattended; the leadership transfer
  and the leader's own restart require explicit operator confirmation.
- **No automatic version rollback.** Downgrading a raft member is not obviously
  safe, so a failed upgrade stops and reports rather than reversing itself.
  Compensations restore orchestration state (drain, timers), not versions.

## Repository rule

**No cluster specifics belong in this repository.** No hostnames, no addresses,
no node names, no counts. Topology is discovered at runtime, so a literal here
is both unnecessary and a leak. Tests use fixtures, never a real inventory.

## Usage

```
hashi-upgrade --help
```

## Development

```
make help        # list targets
make check       # fast local loop: tests + vet
make test        # race detector and coverage
make lint        # golangci-lint
make govulncheck # dependency vulnerability scan
```

## License

MIT. See [LICENSE](LICENSE).
