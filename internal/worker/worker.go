package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/easypodcasts/go-worker/pkg/config"
	"github.com/easypodcasts/go-worker/pkg/log"
	"github.com/easypodcasts/go-worker/pkg/network"
	"github.com/sirupsen/logrus"
)

const (
	ShutdownTimeout  = 30
	MinCheckInterval = 3
)

type Worker struct {
	c      config.Config
	client *network.Client
	sem    chan struct{}

	// abortBuilds is used to abort running builds
	abortBuilds chan os.Signal

	// stopSignals is to catch a signals notified to process: SIGTERM, SIGQUIT, Interrupt, Kill
	stopSignals chan os.Signal

	// stopSignal is used to preserve the signal that was used to stop the
	// process In case this is SIGQUIT it makes to finish all builds and session
	// server.
	stopSignal os.Signal

	// runFinished is used to notify that run() did finish
	runFinished chan bool

	// startShutdown is used to notify the beginning of shutdown
	startShutdown chan struct{}
}

type Job struct {
	ID  int
	URL string
}

func NewWorker(c config.Config) *Worker {
	client, err := network.NewClient(c.Endpoint, network.DefaultTimeout, func() string {
		return c.Token
	})
	if err != nil {
		return nil
	}

	return &Worker{
		c:      c,
		client: client,
		sem:    make(chan struct{}, c.Limit),
	}
}

func (w *Worker) Run() error {
	if w.c.BuildsDir != "" {
		err := os.Chdir(w.c.BuildsDir)
		if err != nil {
			return err
		}
	}

	w.abortBuilds = make(chan os.Signal)
	w.runFinished = make(chan bool, 1)
	w.stopSignals = make(chan os.Signal)
	w.startShutdown = make(chan struct{}, 1)

	go func() {
		runners := make(chan Job)
		go w.feedRunners(runners)

		stopWorker := make(chan bool)
		w.startWorkers(stopWorker, runners)

		// Block until shutdown is started
		<-w.startShutdown

		// Wait for workers to shutdown
		for currentWorkers := w.c.Limit; currentWorkers > 0; currentWorkers-- {
			stopWorker <- true
		}

		log.L.Info("All workers stopped. Can exit now")

		close(w.runFinished)
	}()

	return nil
}

func (w *Worker) Shutdown(signal os.Signal) error {
	close(w.startShutdown)

	if w.stopSignal == nil {
		w.stopSignal = signal
	}

	// On Windows, we convert SIGTERM and SIGINT signals into a SIGQUIT.
	//
	// This enforces *graceful* termination on the first signal received, and a forceful shutdown
	// on the second.
	//
	// This slightly differs from other operating systems. On other systems, receiving a SIGQUIT
	// works the same way (gracefully) but receiving a SIGTERM and SIGQUIT always results
	// in an immediate forceful shutdown.
	//
	// This handling has to be different as SIGQUIT is not a signal the os/signal package translates
	// any Windows control concepts to.
	if runtime.GOOS == "windows" {
		w.stopSignal = syscall.SIGQUIT
	}

	err := w.handleGracefulShutdown()
	if err == nil {
		return nil
	}

	log.L.
		WithError(err).
		Warning("Graceful shutdown not finished properly")

	err = w.handleForcefulShutdown()
	if err == nil {
		return nil
	}

	log.L.
		WithError(err).
		Warning("Forceful shutdown not finished properly")

	return err

}

// feedRunners works until a stopSignal was saved.
// It is responsible for feeding the jobs to channel, which
// asynchronously ends jobs being executed by concurrent workers.
// This is also the place where check interval is calculated and
// applied.
func (w *Worker) feedRunners(jobs chan Job) {
	for w.stopSignal == nil {
		log.L.Debugln("Feeding jobs to channel")

		interval := time.Duration(w.c.CheckInterval) * time.Second / time.Duration(w.c.Limit)

		if interval < MinCheckInterval {
			interval = MinCheckInterval
		}

		// fetchJob

		// Feed runner with waiting exact amount of time
		jobs <- Job{
			ID:  1,
			URL: "https://testing.com",
		}
		log.L.Debugln("Sleep", interval)
		time.Sleep(interval)
	}

	log.L.
		WithField("StopSignal", w.stopSignal).
		Debug("Stopping feeding jobs to channel")
}

// startWorkers is responsible for starting the workers (up to the number
// defined by `Limit`) and assigning a runner processing method to them.
func (w *Worker) startWorkers(stopWorker chan bool, jobs chan Job) {
	for i := 0; i < w.c.Limit; i++ {
		go w.processRunners(i, stopWorker, jobs)
	}
}

// processRunners is responsible for processing a Runner on a worker (when received
// a runner information sent to the channel by feedRunners) and for terminating the worker
// (when received an information on stoWorker chan - provided by updateWorkers)
func (w *Worker) processRunners(id int, stopWorker chan bool, jobs chan Job) {
	ctx := log.WithLogger(context.Background(), logrus.WithField("worker", id))
	log.G(ctx).Debugln("Starting worker")

	for w.stopSignal == nil {
		select {
		case job := <-jobs:
			err := w.processJob(ctx, job)
			if err != nil {
				log.G(ctx).WithFields(logrus.Fields{
					"job": job.ID,
				}).WithError(err)

				log.G(ctx).Warn("Failed to process job")
			}

			// force GC cycle after processing
			runtime.GC()

		case <-stopWorker:
			log.G(ctx).
				WithField("worker", id).
				Debugln("Stopping worker")
			return
		}
	}
	<-stopWorker
}

// processJob is responsible for handling one job on a specified runner.
func (w *Worker) processJob(ctx context.Context, job Job) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	runFinish := make(chan error, 1)
	runPanic := make(chan error, 1)

	runContext, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// Run ffmpeg script
	go func() {
		defer func() {
			if r := recover(); r != nil {
				//runPanic <- &BuildError{FailureReason: RunnerSystemFailure, Inner: fmt.Errorf("panic: %s", r)}
			}
		}()

		runFinish <- executeScript(runContext, job)
	}()

	// Wait for signals: cancel, timeout, abort or finish
	log.G(ctx).Debugln("Waiting for signals...")
	select {
	case <-ctx.Done():
		return ctx.Err()

	case signal := <-w.abortBuilds:
		_ = signal
		//err = &BuildError{
		//	Inner:         fmt.Errorf("aborted: %v", signal),
		//	FailureReason: RunnerSystemFailure,
		//}
		//b.setCurrentState(BuildRunRuntimeTerminated)

	case err = <-runFinish:
		if err != nil {
			//b.setCurrentState(BuildRunRuntimeFailed)
		} else {
			//b.setCurrentState(BuildRunRuntimeSuccess)
		}
		return err

	case err = <-runPanic:
		//b.setCurrentState(BuildRunRuntimeTerminated)
		return err
	}

	log.G(ctx).WithError(err).Debugln("Waiting for build to finish...")

	select {
	case <-runFinish:
		return
	case <-ctx.Done():
		log.G(ctx).Warningln("Timed out waiting for the build to finish")
		return
	}
}

func executeScript(ctx context.Context, job Job) error {
	time.Sleep(20 * time.Second)
	return nil
}

// handleGracefulShutdown is responsible for handling the "graceful" strategy of exiting.
// It's executed only when specific signal is used to terminate the process.
// At this moment feedRunners() should exit and workers scaling is being terminated.
// This means that new jobs will be not requested. handleGracefulShutdown() will ensure that
// the process will not exit until `mr.runFinished` is closed, so all jobs were finished and
// all workers terminated. It may however exit if another signal - other than the gracefulShutdown
// signal - is received.
func (w *Worker) handleGracefulShutdown() error {
	// We wait till we have a SIGQUIT
	for w.stopSignal == syscall.SIGQUIT {
		log.L.
			WithField("StopSignal", w.stopSignal).
			Warning("Starting graceful shutdown, waiting for builds to finish")

		// Wait for other signals to finish builds
		select {
		case w.stopSignal = <-w.stopSignals:
			// We received a new signal
			log.L.WithField("stop-signal", w.stopSignal).Warning("[handleGracefulShutdown] received stop signal")

		case <-w.runFinished:
			// Everything finished we can exit now
			return nil
		}
	}

	return fmt.Errorf("received stop signal: %v", w.stopSignal)
}

// handleForcefulShutdown is executed if handleGracefulShutdown exited with an error
// (which means that a signal forcing shutdown was used instead of the signal
// specific for graceful shutdown).
// It calls mr.abortAllBuilds which will broadcast abort signal which finally
// ends with jobs termination.
// Next it waits for one of the following events:
// 1. Another signal was sent to process, which is handled as force exit and
//    triggers exit of the method and finally process termination without
//    waiting for anything else.
// 2. ShutdownTimeout is exceeded. If waiting for shutdown will take more than
//    defined time, the process will be forceful terminated just like in the
//    case when second signal is sent.
// 3. mr.runFinished was closed, which means that all termination was done
//    properly.
func (w *Worker) handleForcefulShutdown() error {
	log.L.
		WithField("StopSignal", w.stopSignal).
		Warning("Starting forceful shutdown")

	go w.abortAllBuilds()

	// Wait for graceful shutdown or abort after timeout
	for {
		select {
		case w.stopSignal = <-w.stopSignals:
			log.L.WithField("stop-signal", w.stopSignal).Warning("[handleForcefulShutdown] received stop signal")
			return fmt.Errorf("forced exit with stop signal: %v", w.stopSignal)

		case <-time.After(ShutdownTimeout * time.Second):
			return errors.New("shutdown timed out")

		case <-w.runFinished:
			// Everything finished we can exit now
			return nil
		}
	}
}

// abortAllBuilds broadcasts abort signal, which ends with all currently executed
// jobs being interrupted and terminated.
func (w *Worker) abortAllBuilds() {
	log.L.Debug("Broadcasting job abort signal")

	// Pump signal to abort all current builds
	for {
		w.abortBuilds <- w.stopSignal
	}
}
