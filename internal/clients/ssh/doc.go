// Package ssh runs commands on cluster nodes over a certificate-authenticated
// connection.
//
// It exists because a converge cannot be triggered through an API: the cinc
// server is polled by its nodes and offers nothing to push at them, so the
// parts of a run that act on a node act on it over SSH. The package is the
// transport and nothing else. It decides no order, judges no output and knows
// nothing about upgrades; callers hand it a host and a command and receive
// what the host said and how it exited.
//
// A certificate is offered ahead of the bare key. The two are otherwise
// interchangeable to the client and not to the server: a node whose
// authorized_keys carries a forced command answers the key with that command
// rather than with the one asked for, and the certificate path bypasses the
// file entirely.
package ssh
