#!/usr/bin/env bash
# -------------------------------------------------------------------------------
# Preflight - Can This Run Actually Reach Its Hosts
#
# Author: Alex Freidah
#
# A dry run proves the cluster answers, and nothing else: it builds no ssh
# client and no configuration-server client, so the two things that touch hosts
# go untested until a live run is already underway -- by which point the pin has
# moved and the timers are off.
#
# This closes that gap. For every host in a run file it reaches the address the
# run would dial, as the user the run would use, and asks the two questions a
# converge depends on: is cinc-client there, and is its timer running.
#
# Reads the addresses from the run file rather than an inventory, so it checks
# what the run will actually do and holds no cluster specifics of its own.
#
#   scripts/preflight.sh <run-file> [ssh-user]
# -------------------------------------------------------------------------------

set -uo pipefail

readonly run_file="${1:-}"
readonly ssh_user="${2:-root}"

if [[ -z "$run_file" || ! -f "$run_file" ]]; then
  echo "usage: $0 <run-file> [ssh-user]" >&2
  exit 2
fi

# --- Hosts -------------------------------------------------------------------
#
# Pulled from the survey the run was generated against. The name is for
# reading; the address is what resolver hands to ssh.

hosts=$(awk '
  /^ +- id:/           { name=""; addr="" }
  /^ +name:/           { name=$2 }
  /^ +addr:/           { addr=$2; if (name != "" && addr != "") print name, addr }
' "$run_file")

if [[ -z "$hosts" ]]; then
  echo "no hosts found in $run_file" >&2
  exit 2
fi

printf 'checking %d hosts from %s as %s\n\n' \
  "$(wc -l <<<"$hosts")" "$run_file" "$ssh_user"

# --- Checks ------------------------------------------------------------------
#
# One connection per host, asking exactly what a converge needs. A host that
# answers with a version and an active timer is one the run can drive.

failed=0

while read -r name addr; do
  printf '%-18s %-14s ' "$name" "$addr"

  # -n matters: without it ssh reads the host list off stdin and the loop
  # checks one host and stops.
  out=$(ssh -n \
            -o BatchMode=yes \
            -o ConnectTimeout=8 \
            -o StrictHostKeyChecking=accept-new \
            "${ssh_user}@${addr}" \
            'printf "%s|%s" "$(cinc-client --version 2>/dev/null)" "$(systemctl is-active cinc-client.timer 2>/dev/null)"' \
         2>&1)
  rc=$?

  if (( rc != 0 )); then
    printf 'UNREACHABLE  %s\n' "$(tail -1 <<<"$out")"
    failed=$((failed + 1))
    continue
  fi

  version="${out%%|*}"
  timer="${out##*|}"

  if [[ -z "$version" ]]; then
    printf 'NO CINC-CLIENT\n'
    failed=$((failed + 1))
    continue
  fi

  # An inactive timer is not a failure. Freeze stops it anyway, and a host
  # whose timer is already off is the state freeze is trying to reach -- but it
  # is worth seeing, because a fleet with timers off was left that way by
  # something.
  printf '%-22s timer=%s\n' "$version" "${timer:-unknown}"
done <<<"$hosts"

# --- Verdict -----------------------------------------------------------------

echo
if (( failed > 0 )); then
  printf '%d of %d hosts would fail the run\n' "$failed" "$(wc -l <<<"$hosts")"
  exit 1
fi

printf 'all %d hosts reachable with cinc-client present\n' "$(wc -l <<<"$hosts")"
