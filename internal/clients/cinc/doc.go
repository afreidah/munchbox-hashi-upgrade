// Package cinc reads and writes the version a cluster is pinned to.
//
// The pin lives in a data bag on the configuration server, one item per tool.
// The cookbooks read it and install drift-only, so writing the pin is the act
// that arms an upgrade: a converge against an unchanged pin is a genuine
// no-op, and a converge after one is what moves a node.
//
// It is the only package that imports the CINC API, so the planner and the
// runner never learn what a data bag item is. What the item looks like is not
// this package's to choose -- the cookbook that reads it decides, and this
// package matches it.
//
// Arming an upgrade is an API call and applying it is not: the server is polled
// by its nodes and offers nothing to push at them. Fleet is that half, over SSH
// -- the converge itself, and the hourly timer that has to be down first so a
// scheduled run does not apply a pin the rollout has not reached yet.
package cinc
