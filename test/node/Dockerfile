# -------------------------------------------------------------------------------
# Test Node - Nomad, An SSH Server, And Stand-Ins For The Two System Services
#
# Author: Alex Freidah
#
# A node the tool can drive the way it drives a real one: reach it over ssh with
# a certificate-verified host key, run `cinc-client` on it, and watch its Nomad
# version change.
#
# Two stand-ins, both for things a container does not have:
#
#   systemctl    the timer commands Freeze and Thaw issue. A flag file, since
#                what the tool needs is a unit that reports enabled or not.
#   cinc-client  reads the pinned version from the configuration server and
#                installs it. Not Chef -- what this proves is the orchestration
#                around a converge, not the converge itself.
#
# Nomad runs under a supervisor loop rather than as PID 1, so the shim can
# restart it without the container exiting.
# -------------------------------------------------------------------------------

ARG FROM_VERSION=2.0.5

# The official image carries no package manager -- no /etc/os-release and no
# apk -- so the binary is lifted out of it onto a base that can install an ssh
# server. The version under test is still the one that image ships.
FROM hashicorp/nomad:${FROM_VERSION} AS nomad

# Debian rather than Alpine: the released binary is linked against glibc, and on
# musl it fails as "not found" -- the loader is missing, not the file.
FROM debian:trixie-slim

# unzip and curl install a release; openssh serves the tool; jq reads the pin.
#
# iproute2 is not optional: the client half shells out to /sbin/ip to fingerprint
# its network, and without it the agent exits with "Error while detecting network
# interface" and registers no node at all.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      openssh-server curl unzip jq procps ca-certificates iproute2 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=nomad /bin/nomad /bin/nomad

# The host key every node presents, and the certificate the tool verifies it by.
# One key across the fleet: these are containers on a private network, and the
# tool's requirement is a cert signed by a CA it trusts, not a distinct key per
# host.
COPY keys/host_key /etc/ssh/ssh_host_ed25519_key
COPY keys/host_key-cert.pub /etc/ssh/ssh_host_ed25519_key-cert.pub
COPY keys/user_key.pub /root/.ssh/authorized_keys

COPY sbin/ /usr/local/sbin/

RUN chmod 600 /etc/ssh/ssh_host_ed25519_key /root/.ssh/authorized_keys \
 && chmod 755 /usr/local/sbin/cinc-client /usr/local/sbin/systemctl /usr/local/sbin/node-entrypoint \
 && printf '%s\n' \
      'HostKey /etc/ssh/ssh_host_ed25519_key' \
      'HostCertificate /etc/ssh/ssh_host_ed25519_key-cert.pub' \
      'PermitRootLogin yes' \
      'PubkeyAuthentication yes' \
      'PasswordAuthentication no' \
      > /etc/ssh/sshd_config \
 && mkdir -p /var/cinc/cache /run/sshd

# Ahead of /bin, so `systemctl` and `cinc-client` resolve to the stand-ins.
ENV PATH="/usr/local/sbin:${PATH}"

ENTRYPOINT ["/usr/local/sbin/node-entrypoint"]
