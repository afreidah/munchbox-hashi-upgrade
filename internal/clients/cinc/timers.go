// -------------------------------------------------------------------------------
// Timers - Freezing And Thawing The Scheduled Converge
//
// Author: Alex Freidah
//
// The hourly timer is what makes an upgrade racy: a converge that fires on a
// node partway through a rollout applies a cookbook the run has not promoted
// yet. Freeze stops it everywhere and confirms both halves of the condition -
// the timer is down, and nothing is already converging.
//
// Thaw is the compensation and runs on every node whether its freeze succeeded
// or not, because leaving automation stopped fleet-wide is worse than enabling
// a timer that was already enabled.
// -------------------------------------------------------------------------------

package cinc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/ssh"
)

// Exit code flock reports when the lock is held, distinct from the codes it
// uses for its own failures so a busy node is told apart from a broken one.
const lockBusyCode = 9

// How long a converge already under way is given to finish, and how often it
// is looked at. Long enough for a real cookbook run on a slow host; short
// enough that a lock left behind by something wedged does not hold a fleet-wide
// upgrade open indefinitely.
const (
	convergeWait = 15 * time.Minute
	convergePoll = 10 * time.Second
)

// Conditions a freeze refuses on, rather than the command itself failing.
var (
	ErrConvergeInFlight = errors.New("converge already running")
	ErrTimerActive      = errors.New("timer still active after disable")
)

// NodeError is a failure attributed to the node it happened on.
type NodeError struct {
	Target ssh.Target
	Err    error
}

// Error names the node, since a fleet-wide failure is unreadable without it.
func (e *NodeError) Error() string {
	return fmt.Sprintf("%s: %v", e.Target.Host, e.Err)
}

// Unwrap returns the underlying failure.
func (e *NodeError) Unwrap() error { return e.Err }

// Freeze stops the scheduled converge on every target and confirms it stopped.
//
// A node mid-converge is waited for rather than refused. The timers are on a
// schedule and a fleet is many hosts, so at any moment one of them is usually
// running: failing on that would make freezing a matter of retrying until the
// gaps happened to line up. The timer is disabled first, so nothing new starts
// while the wait is on, and what is left is bounded by the converge already
// under way.
func (f *Fleet) Freeze(ctx context.Context, targets []ssh.Target) error {
	return f.each(ctx, targets, func(ctx context.Context, s Session) error {
		if _, err := must(ctx, s, "systemctl disable --now "+timerUnit); err != nil {
			return err
		}

		// --- is-active exits non-zero on a stopped unit, so a clean exit here
		//     is the failure: the timer survived the disable. ---
		if res, err := run(ctx, s, "systemctl is-active "+timerUnit); err != nil {
			return err
		} else if res.OK() {
			return ErrTimerActive
		}

		return f.awaitIdle(ctx, s)
	})
}

// awaitIdle waits for a converge already running to finish.
//
// Reported when it has to wait, and only then: a fleet where nothing is running
// should say nothing, and a run that appears to hang for ten minutes on its
// first task is worse than one that says what it is waiting for.
func (f *Fleet) awaitIdle(ctx context.Context, s Session) error {
	deadline := time.Now().Add(f.wait)

	for said := false; ; {
		err := inFlight(ctx, s)
		if !errors.Is(err, ErrConvergeInFlight) {
			if said {
				f.sayf("    converge finished\n")
			}
			return err
		}

		if !said {
			f.sayf("    a converge is already running; waiting for it to finish\n")
			said = true
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%w after %s", ErrConvergeInFlight, f.wait)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(f.poll):
		}
	}
}

// Thaw starts the scheduled converge on every target.
//
// Every target is attempted even once one has failed, and enabling a timer that
// is already enabled is a no-op, so this is safe to call after a partial freeze.
func (f *Fleet) Thaw(ctx context.Context, targets []ssh.Target) error {
	return f.each(ctx, targets, func(ctx context.Context, s Session) error {
		_, err := must(ctx, s, "systemctl enable --now "+timerUnit)
		return err
	})
}

// inFlight reports whether a converge holds the run lock.
//
// The lock file outlives the run that made it and keeps the pid of a process
// that has exited, so its existence proves nothing; taking the lock does.
func inFlight(ctx context.Context, s Session) error {
	res, err := run(ctx, s, fmt.Sprintf("flock -n -E %d %s true", lockBusyCode, runLock))
	switch {
	case err != nil:
		return err
	case res.Code == lockBusyCode:
		return ErrConvergeInFlight
	case !res.OK():
		return fmt.Errorf("checking the run lock: exit %d: %s", res.Code, trim(res.Output))
	}

	return nil
}

// each runs fn against every target at once and joins what failed.
func (f *Fleet) each(ctx context.Context, targets []ssh.Target, fn func(context.Context, Session) error) error {
	errs := make([]error, len(targets))

	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Go(func() {
			errs[i] = f.on(ctx, target, fn)
		})
	}
	wg.Wait()

	return errors.Join(errs...)
}

// on opens a session to target and hands it to fn, attributing any failure.
func (f *Fleet) on(ctx context.Context, target ssh.Target, fn func(context.Context, Session) error) error {
	session, err := f.dial.Connect(target)
	if err != nil {
		return &NodeError{Target: target, Err: err}
	}
	defer func() { _ = session.Close() }()

	if err := fn(ctx, session); err != nil {
		return &NodeError{Target: target, Err: err}
	}

	return nil
}

// run runs cmd without streaming it anywhere, returning the result for the
// caller to decide about. These commands answer in one line and there is
// nothing to watch.
func run(ctx context.Context, s Session, cmd string) (ssh.Result, error) {
	return s.Run(ctx, cmd, nil)
}

// must is run for a command whose exit status carries no meaning beyond
// success, folding a non-zero exit into the error.
func must(ctx context.Context, s Session, cmd string) (ssh.Result, error) {
	res, err := run(ctx, s, cmd)
	if err != nil {
		return res, err
	}
	if !res.OK() {
		return res, fmt.Errorf("%s: exit %d: %s", cmd, res.Code, trim(res.Output))
	}

	return res, nil
}

// trim reduces command output to one line, so a wrapped error stays readable.
func trim(out string) string {
	return strings.Join(strings.Fields(out), " ")
}
