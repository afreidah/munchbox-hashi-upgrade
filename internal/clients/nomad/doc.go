// Package nomad answers the questions the tool asks about a Nomad cluster.
//
// It is the only package that imports the Nomad API. Callers ask for a survey
// or a health verdict and receive neutral types, so the planner and the runner
// never learn what an autopilot reply or a node stub is, and the Consul and
// Vault packages can answer the same questions against their own libraries.
//
// Where Nomad's own view is split across endpoints, the reconciling happens
// here. A host that runs as both a server and a client appears in two of
// Nomad's lists and is reported once, as a server, because it holds one binary
// and one service.
package nomad
