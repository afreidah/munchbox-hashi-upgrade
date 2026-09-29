// Package runner drives a run file to completion, one task at a time.
//
// The loop is: read the next unsettled task, carry it out, record the outcome,
// write the file, repeat. The file is written after every task, so a run that
// stops for any reason is resumed by opening it again.
//
// A task that fails stops the run and unwinds the compensations stacked by the
// tasks that already succeeded, in reverse. Compensations restore orchestration
// state -- release the scheduled converges, make a drained host eligible again.
// None of them puts a version back: a host running the new binary is running
// it.
//
// Nothing here knows how a task is carried out. A task names a command, the
// caller supplies the step that implements it, and this package owns order,
// persistence and what a failure costs.
package runner
