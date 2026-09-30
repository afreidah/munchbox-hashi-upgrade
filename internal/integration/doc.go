// -------------------------------------------------------------------------------
// Integration Tests - Against A Real Cluster
//
// Author: Alex Freidah
//
// Everything here runs against Nomad in a container rather than against a
// stand-in. The client package's unit tests cover how two API reads are
// reconciled into a snapshot, which is the part worth testing in isolation;
// what they cannot cover is whether the calls are the ones Nomad answers, or
// whether a drain that reports finished has actually finished. Those are
// properties of the cluster, and only a cluster settles them.
//
// Everything is behind the `integration` build tag, so `go test ./...` stays
// free of Docker. `make integration-test` runs them; `make integration-coverage`
// writes a profile of its own that the coverage dashboard merges with the unit
// one, since a line these tests reach is covered whichever tier reached it.
//
// The containers are shared across the package. A cluster costs seconds to
// start where the state a test needs costs milliseconds, so tests declare the
// state they want rather than the cluster they want.
// -------------------------------------------------------------------------------

package integration
