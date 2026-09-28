// -------------------------------------------------------------------------------
// Cinc Test Doubles - Mock Generation Surface
//
// Author: Alex Freidah
//
// Declares the go:generate directive that builds mocks for the transport
// interfaces internal/clients/cinc consumes. They live in their own package so
// the runner can stand a fleet up over a fake connection: a converge takes
// minutes, needs a signing key and an SSH certificate, and changes the machine
// it runs on.
// -------------------------------------------------------------------------------

package cincmock

//go:generate mockgen -destination=mocks.go -package=cincmock github.com/afreidah/munchbox-hashi-upgrade/internal/clients/cinc Dialer,Session
