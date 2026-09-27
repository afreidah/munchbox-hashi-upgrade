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
package cinc
