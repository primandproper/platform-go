package service

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// Run starts what New built, blocks until the process is asked to stop, and
// then takes it all down.
//
// It returns when either SIGINT or SIGTERM arrives, when the context it was
// given is cancelled, or when a server stops serving on its own — a bound port
// that is no longer answering is not a state to keep the rest of the process
// alive for, so a Serve that returns early takes the service down with it and
// its error is reported alongside the shutdown's.
//
// The signal notification is released as soon as the shutdown begins, which
// restores the default disposition for both signals. That is deliberate: a
// drain that will not finish must still be killable by the second Ctrl-C, or by
// the SIGKILL an orchestrator sends when the grace period runs out.
//
// Shutdown runs on a context stripped of the cancellation that triggered it.
// Draining on a cancelled context would cancel every drain it is made of, which
// is the opposite of a graceful stop; the bound on how long it may take is
// Config.ShutdownTimeout instead.
func (s *Service) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	finish := func(err error) error {
		stop()

		return platformerrors.Join(err, s.Shutdown(context.WithoutCancel(ctx)))
	}

	// The profiler is the one pillar with a start of its own, and it starts
	// first for the same reason Pillars.Shutdown runs last: a process is worth
	// observing from before it serves until after it stops.
	if s.pillars.Profiler != nil {
		if err := s.pillars.Profiler.Start(ctx); err != nil {
			return finish(platformerrors.Wrap(err, "starting the profiler"))
		}
	}

	for _, runner := range s.runners {
		s.logger.WithValue("runner", runner.name).Debug("starting background loop")

		go runner.v.Run()
	}

	// Serve blocks, so each server gets a goroutine. The channel is buffered to
	// one slot per server: shutdown stops reading after the first result, and
	// an unbuffered channel would strand the other goroutines forever.
	type serveResult = named[error]

	results := make(chan serveResult, len(s.servers))
	for _, server := range s.servers {
		s.logger.WithValue("server", server.name).Info("serving")

		go func() { results <- serveResult{name: server.name, v: server.v.Serve(ctx)} }()
	}

	var err error

	select {
	case <-ctx.Done():
		s.logger.Info("shutting down")
	case result := <-results:
		s.logger.WithValue("server", result.name).Info("server stopped serving")

		if result.v != nil {
			err = platformerrors.Wrapf(result.v, "serving the %s", result.name)
		}
	}

	return finish(err)
}

// Shutdown takes the service down in the order that makes each step's work mean
// something, and reports everything that failed rather than the first thing.
//
// The order is the point:
//
//  1. Ingress, so nothing new arrives. Every drain below this line is chasing a
//     moving target until it happens, and the servers go down together rather
//     than in turn because they are independent and share one budget — draining
//     HTTP for the whole of it would leave gRPC nothing but a hard stop.
//  2. The background loops, in reverse start order, so each one's final cycle
//     runs after everything that feeds it has stopped. This is what the outbox
//     relay's Run-takes-no-context comment is about: its last cycle sees every
//     row the last requests committed.
//  3. The single-shot drains, which need the clients below them and the
//     producers above them to be finished.
//  4. The clients, in reverse build order, so the database client — which
//     anything above it may have used on the way out — is released last.
//  5. The observability pillars, so everything this sequence logged, traced,
//     and measured is exported rather than dropped on the floor with it.
//
// The whole sequence shares one deadline, Config.ShutdownTimeout, applied to
// whatever context it is given. Phases therefore compete: one that overruns
// leaves less for the ones after it, which is the second reason the order is
// what it is. The later phases still run on the expired context — a Close that
// takes no context still releases its handle, and one that does reports the
// deadline instead of hanging.
//
// Shutdown is safe to call more than once and from anywhere; repeat calls
// report the first run's result rather than taking a shut service down twice.
// Run calls it, so a caller that uses Run does not.
func (s *Service) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() { s.shutdownErr = s.shutdown(ctx) })

	return s.shutdownErr
}

func (s *Service) shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.shutdownTimeout)
	defer cancel()

	errs := s.stopServers(ctx)

	for idx := len(s.runners) - 1; idx >= 0; idx-- {
		runner := s.runners[idx]

		s.logger.WithValue("runner", runner.name).Debug("closing background loop")

		if err := runner.v.Close(ctx); err != nil {
			errs = append(errs, s.report(err, "closing the %s", runner.name))
		}
	}

	for _, flush := range s.flushes {
		s.logger.WithValue("flush", flush.name).Debug("running final flush")

		if err := flush.v(ctx); err != nil {
			errs = append(errs, s.report(err, "running the final %s pass", flush.name))
		}
	}

	for idx := len(s.closers) - 1; idx >= 0; idx-- {
		closer := s.closers[idx]

		s.logger.WithValue("client", closer.name).Debug("releasing client")

		if err := closer.v(ctx); err != nil {
			errs = append(errs, s.report(err, "releasing the %s", closer.name))
		}
	}

	s.logger.Info("shutdown complete")

	// Last, and after the line above: the pillars are what carried every
	// message this sequence produced, and shutting them down first would make
	// the shutdown the one part of a process's life that is invisible.
	if err := s.pillars.Shutdown(ctx); err != nil {
		errs = append(errs, platformerrors.Wrap(err, "shutting down the observability pillars"))
	}

	return platformerrors.Join(errs...)
}

// stopServers drains ingress, all of it at once, and returns one error per
// server that did not stop cleanly — in the order the servers were built, not
// the order they happened to finish, so the same failure reads the same way
// twice.
func (s *Service) stopServers(ctx context.Context) []error {
	errs := make([]error, len(s.servers))

	var wg sync.WaitGroup

	for idx, server := range s.servers {
		wg.Go(func() {
			s.logger.WithValue("server", server.name).Debug("draining server")

			if err := server.v.Shutdown(ctx); err != nil {
				errs[idx] = s.report(err, "shutting down the %s", server.name)
			}
		})
	}

	wg.Wait()

	return errs
}

// report logs a shutdown failure and returns it wrapped.
//
// Both, because a shutdown error has two audiences that rarely overlap: Run's
// return value, which a main() may or may not print before exiting, and the
// logs, which are where anyone diagnosing a process that would not stop
// cleanly actually looks.
func (s *Service) report(err error, format string, args ...any) error {
	wrapped := platformerrors.Wrapf(err, format, args...)

	s.logger.Error("shutting down", wrapped)

	return wrapped
}
