// Package ready decides whether a run may proceed to the next host.
//
// A step reporting success is not evidence the cluster absorbed it. A converge
// exits zero once the new binary is installed and the agent restarted, which is
// the moment before everything worth checking: whether the host rejoined, what
// the rest of the cluster makes of it, and whether coordination can survive
// losing another one. So the gates are read separately from the step that
// caused them, from the cluster rather than from the step's own output.
//
// What they assert is what an operator reads by hand between nodes. The cluster
// already computes its own verdicts -- autopilot folds trailing log distance,
// last contact and a stabilisation period into one answer -- so these read that
// verdict rather than deriving a second one from the fields underneath it.
//
// Every gate waits. A host that has just restarted is expected to be briefly
// unfit, and a gate that refused on the first read would fail every upgrade it
// was meant to protect.
package ready
