// Package plan describes an upgrade run and keeps it on disk.
//
// A run pairs three things: the spec being carried out, the cluster as it was
// surveyed when the run was generated, and the ordered tasks that carry it
// out. Those three are fixed once written. What happened is kept separately,
// as a record per task id, so progress accumulates against an unchanging plan
// rather than rewriting it.
//
// That split is what makes a run resumable and reviewable at the same time. A
// resumed run picks up at the first task without a settled record, and a run
// can be compared against a freshly generated one for the same cluster without
// progress from the first getting in the way.
//
// Nothing here reaches the network or decides an order. Discovery produces the
// cluster survey and the steps package derives the task list from it; this
// package only describes what they produce and stores it safely.
package plan
