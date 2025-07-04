package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/buger/goterm"
	"github.com/easypodcasts/go-worker/pkg/config"
	"github.com/easypodcasts/go-worker/pkg/ffmpeg"
	"github.com/easypodcasts/go-worker/pkg/log"
	"github.com/easypodcasts/go-worker/pkg/network"
	"github.com/easypodcasts/go-worker/pkg/progress"
	jsoniter "github.com/json-iterator/go"
	"github.com/sirupsen/logrus"
)

const (
	ShutdownTimeout  = 30
	MinCheckInterval = 3
)

var (
	ErrEmptyJob     = errors.New("no episodes to convert")
	ErrUnauthorized = errors.New("check your token")
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

func NewWorker(c config.Config) (*Worker, error) {
	endpoint, err := url.Parse(c.Endpoint)
	if err != nil {
		return nil, err
	}
	client, err := network.NewClient(*endpoint, network.DefaultTimeout, func() string {
		return c.Token
	})
	if err != nil {
		return nil, nil
	}

	return &Worker{
		c:      c,
		client: client,
		sem:    make(chan struct{}, c.Limit),
	}, nil
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
		_ = progress.Run(context.Background(), func(ctxRun context.Context) error {

			runners := make(chan Job)
			go w.feedRunners(runners)

			stopWorker := make(chan struct{})
			w.startWorkers(ctxRun, stopWorker, runners)

			// Block until shutdown is started
			<-w.startShutdown

			// Wait for workers to shutdown
			for currentWorkers := w.c.Limit; currentWorkers > 0; currentWorkers-- {
				stopWorker <- struct{}{}
			}

			log.L.Info("All workers stopped. Can exit now")

			close(w.runFinished)
			return nil
		})
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
		w.sem <- struct{}{}

		log.L.Debugln("Feeding jobs to channel")

		interval := time.Duration(w.c.CheckInterval) * time.Second / time.Duration(w.c.Limit)

		if interval < MinCheckInterval {
			interval = MinCheckInterval
		}

		// Feed runner
		job, err := w.fetchJob(context.Background())
		if err != nil {
			switch err {
			case ErrEmptyJob, ErrUnauthorized:
				log.L.WithError(err).Debugln()
			default:
				log.L.WithError(err).Errorln()
			}
		} else {
			jobs <- job
		}

		<-w.sem
		log.L.Debugln("Sleep", interval)
		time.Sleep(interval)
	}

	log.L.
		WithField("StopSignal", w.stopSignal).
		Debug("Stopping feeding jobs to channel")
}

// startWorkers is responsible for starting the workers (up to the number
// defined by `Limit`) and assigning a runner processing method to them.
func (w *Worker) startWorkers(ctx context.Context, stopWorker chan struct{}, jobs chan Job) {
	for i := 0; i < w.c.Limit; i++ {
		go w.processRunners(ctx, i, stopWorker, jobs)
	}
}

// processRunners is responsible for processing a Runner on a worker (when received
// a runner information sent to the channel by feedRunners) and for terminating the worker
// (when received an information on stoWorker chan - provided by updateWorkers)
func (w *Worker) processRunners(ctx context.Context, id int, stopWorker chan struct{}, jobs chan Job) {
	ctxLog := log.WithLogger(context.Background(), logrus.WithField("worker", id))
	log.G(ctxLog).Debugln("Starting worker")

	for w.stopSignal == nil {
		select {
		case job := <-jobs:
			err := w.processJob(ctx, job)
			if err != nil {
				w.cancelJob(ctx, job)
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
	w.sem <- struct{}{}
	defer func() { <-w.sem }()

	writer := progress.ContextWriter(ctx)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	runFinish := make(chan error, 1)
	runPanic := make(chan error, 1)

	runContext, runCancel := context.WithCancel(ctx)
	defer runCancel()

	dir, err := os.MkdirTemp("", "easypodcast")
	if err != nil {
		log.G(ctx).WithError(err).Errorln()
		return err
	}
	defer func(path string) {
		_ = os.RemoveAll(path)
	}(dir)

	// Run ffmpeg script
	go func() {
		defer func() {
			if r := recover(); r != nil {
				runPanic <- fmt.Errorf("panic: %s", r)
			}
		}()

		runFinish <- executeScript(runContext, dir, job)
	}()

	// Wait for signals: cancel, timeout, abort or finish
	log.G(ctx).WithError(err).Debugln("Waiting for build to finish...")
	select {
	case <-ctx.Done():
		writer.Event(progress.NewEvent(strconv.Itoa(job.ID), progress.Error, ctx.Err().Error()))
		return ctx.Err()

	case s := <-w.abortBuilds:
		writer.Event(progress.NewEvent(strconv.Itoa(job.ID), progress.Error, s.String()))
		return ctx.Err()

	case err = <-runFinish:
		if err != nil {
			writer.Event(progress.NewEvent(strconv.Itoa(job.ID), progress.Error, err.Error()))
			return err
		}
		writer.Event(progress.NewEvent(strconv.Itoa(job.ID), progress.Working, "Uploading..."))
		err = w.uploadEpisode(ctx, job, dir)
		if err != nil {
			writer.Event(progress.NewEvent(strconv.Itoa(job.ID), progress.Error, err.Error()))
			return err
		}
		writer.Event(progress.NewEvent(strconv.Itoa(job.ID), progress.Done, ""))
		return err

	case err = <-runPanic:
		writer.Event(progress.NewEvent(strconv.Itoa(job.ID), progress.Error, err.Error()))
		return err
	}
}

func executeScript(ctx context.Context, dir string, job Job) error {
	t := ffmpeg.NewTranscoder(job.URL)

	w := progress.ContextWriter(ctx)

	run := t.Run(dir, true)

	for out := range t.Output() {
		pbBox := ""

		if goterm.Width() > 110 {
			numSpaces := 0
			if 50-out.Progress > 0 {
				numSpaces = 50 - out.Progress
			}
			pbBox = fmt.Sprintf("[%s>%s]", strings.Repeat("=", out.Progress), strings.Repeat(" ", numSpaces))
		}

		w.Event(progress.NewEvent(
			strconv.Itoa(job.ID),
			progress.Working,
			fmt.Sprintf("%s %d%%", pbBox, out.Progress),
		))
	}

	err := <-run
	if err != nil {
		return err
	}
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

func (w *Worker) fetchJob(ctx context.Context) (Job, error) {
	respBody, err := w.client.CallRetryable(ctx, http.MethodGet, "api/next", nil, nil, nil)
	if err != nil {
		return Job{}, err
	}
	defer network.DrainBody(respBody)

	body, err := io.ReadAll(respBody)
	if err != nil {
		return Job{}, err
	}

	var job Job

	// RESP can be:
	// noop -> No episodes to convert
	// Unauthorized -> Check your token
	// {id: some_number, url: some url} -> Get to work
	switch string(body) {
	case "\"noop\"":
		return Job{}, ErrEmptyJob
	case "\"Unauthorized\"":
		return Job{}, ErrUnauthorized
	default:
		errDec := jsoniter.Unmarshal(body, &job)
		if errDec != nil {
			return Job{}, errDec
		}
	}

	return job, nil
}

func (w *Worker) uploadEpisode(ctx context.Context, job Job, dir string) error {

	file, err := os.Open(dir + "/episode.mp4")
	if err != nil {
		return err
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	_ = writer.WriteField("id", strconv.Itoa(job.ID))
	part, err := writer.CreateFormFile("audio", filepath.Base(dir+"/episode.mp4"))
	if err != nil {
		return err
	}

	_, err = io.Copy(part, file)
	if err != nil {
		return err
	}

	err = writer.Close()
	if err != nil {
		return err
	}

	h := http.Header{}
	h.Add("Content-Type", writer.FormDataContentType())

	respBody, err := w.client.CallRetryable(ctx, http.MethodPost, "api/converted", h, nil, body)
	if err != nil {
		log.G(ctx).WithError(err).Errorln("Failed to upload")
		return err
	}
	defer network.DrainBody(respBody)

	return nil
}

func (w *Worker) cancelJob(ctx context.Context, job Job) {
	h := http.Header{}
	h.Add("Content-Type", "application/x-www-form-urlencoded")

	form := url.Values{}
	form.Add("id", strconv.Itoa(job.ID))

	respBody, err := w.client.CallRetryable(ctx, http.MethodPost, "api/cancel", h, nil, strings.NewReader(form.Encode()))
	if err != nil {
		log.G(ctx).WithError(err).Errorln("Failed to cancel")
	}
	defer network.DrainBody(respBody)
}
