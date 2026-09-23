// Package steps derives the ordered task list a run carries out.
//
// Build is a pure function of the spec and the cluster survey. Given the same
// survey it always produces the same tasks in the same order, which is what
// lets a generated run be compared against a freshly generated one for the
// same cluster, and what keeps the ordering rules testable against fixtures
// rather than against a cluster.
//
// The rules it encodes are about coordination, not about any particular
// fleet. Servers are upgraded before clients because a cluster tolerates its
// servers running ahead of its clients and not the reverse. Servers go one at
// a time, the coordinating host last, with its coordination handed off first.
// Nothing here consults what a host is running or how busy it is.
package steps
