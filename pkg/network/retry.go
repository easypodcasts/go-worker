package network

import (
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// MaxJitter will randomize over the full exponential backoff time
const MaxJitter = 1.0

// NoJitter disables the use of jitter for randomizing the exponential backoff time
const NoJitter = 0.0

// DefaultRetryUnit - default unit multiplicative per retry.
// defaults to 1 second.
const DefaultRetryUnit = time.Second

// DefaultRetryCap - Each retry attempt never waits no longer than
// this maximum time duration.
const DefaultRetryCap = time.Second * 30

// lockedRandSource provides protected rand source, implements rand.Source interface.
type lockedRandSource struct {
	lk  sync.Mutex
	src rand.Source
}

// Int63 returns a non-negative pseudo-random 63-bit integer as an int64.
func (r *lockedRandSource) Int63() (n int64) {
	r.lk.Lock()
	n = r.src.Int63()
	r.lk.Unlock()

	return
}

// Seed uses the provided seed value to initialize the generator to a
// deterministic state.
func (r *lockedRandSource) Seed(seed int64) {
	r.lk.Lock()
	r.src.Seed(seed)
	r.lk.Unlock()
}

/* #nosec */
// Global random source for fetching random values.
var globalRandomSource = rand.New(&lockedRandSource{
	src: rand.NewSource(time.Now().UTC().UnixNano()),
})

// newRetryTimer creates a timer with exponentially increasing
// delays until the maximum retry attempts are reached.
func newRetryTimerWithJitter(unit time.Duration, cap time.Duration, jitter float64, doneCh chan struct{}) <-chan int {
	attemptCh := make(chan int)

	// normalize jitter to the range [0, 1.0]
	if jitter < NoJitter {
		jitter = NoJitter
	}

	if jitter > MaxJitter {
		jitter = MaxJitter
	}

	// computes the exponential backoff duration according to
	// https://www.awsarchitectureblog.com/2015/03/backoff.html
	exponentialBackoffWait := func(attempt int) time.Duration {
		// 1<<uint(attempt) below could overflow, so limit the value of attempt
		maxAttempt := 30
		if attempt > maxAttempt {
			attempt = maxAttempt
		}
		//sleep = random_between(0, min(cap, base * 2 ** attempt))
		sleep := unit * time.Duration(1<<uint(attempt))
		if sleep > cap {
			sleep = cap
		}

		if jitter != NoJitter {
			sleep -= time.Duration(globalRandomSource.Float64() * float64(sleep) * jitter)
		}

		return sleep
	}

	go func() {
		defer close(attemptCh)

		nextBackoff := 0
		// Channel used to signal after the expiry of backoff wait seconds.
		var timer *time.Timer

		for {
			select { // Attempts starts.
			case attemptCh <- nextBackoff:
				nextBackoff++
			case <-doneCh:
				// Stop the routine.
				return
			}

			timer = time.NewTimer(exponentialBackoffWait(nextBackoff))
			// wait till next backoff time or till doneCh gets a message.
			select {
			case <-timer.C:
			case <-doneCh:
				// stop the timer and return.
				timer.Stop()
				return
			}
		}
	}()

	// Start reading...
	return attemptCh
}

// Default retry constants.
const (
	defaultRetryUnit = time.Second      // 1 second.
	defaultRetryCap  = 30 * time.Second // 30 seconds.
)

// newRetryTimerSimple creates a timer with exponentially increasing delays
// until the maximum retry attempts are reached. - this function is a
// simpler version with all default values.
func newRetryTimerSimple(doneCh chan struct{}) <-chan int {
	return newRetryTimerWithJitter(defaultRetryUnit, defaultRetryCap, MaxJitter, doneCh)
}

// isHTTPReqErrorRetryable - is rest requests error retryable, such
// as i/o timeout, connection broken etc..
func isHTTPReqErrorRetryable(err error) bool {
	if err == nil {
		return false
	}

	switch e := err.(type) {
	case *url.Error:
		switch e.Err.(type) {
		case *net.DNSError, *net.OpError, net.UnknownNetworkError:
			return true
		}

		if strings.Contains(err.Error(), "Connection closed by foreign host") {
			return true
		} else if strings.Contains(err.Error(), "net/rest: TLS handshake timeout") {
			// If error is - tlsHandshakeTimeoutError, retry.
			return true
		} else if strings.Contains(err.Error(), "i/o timeout") {
			// If error is - tcp timeoutError, retry.
			return true
		} else if strings.Contains(err.Error(), "connection timed out") {
			// If err is a net.Dial timeout, retry.
			return true
		} else if strings.Contains(err.Error(), "net/rest: HTTP/1.x transport connection broken") {
			// If error is transport connection broken, retry.
			return true
		}
	}

	return false
}

// List of HTTP status codes which are retryable.
var retryableHTTPStatusCodes = map[int]struct{}{
	http.StatusTooManyRequests:     {},
	http.StatusInternalServerError: {},
	http.StatusBadGateway:          {},
	http.StatusServiceUnavailable:  {},
	// Add more HTTP status codes here.
}

// List of success status.
var successStatus = []int{
	http.StatusOK,
	http.StatusNoContent,
	http.StatusPartialContent,
}

// isHTTPStatusRetryable - is HTTP error code retryable.
func isHTTPStatusRetryable(httpStatusCode int) (ok bool) {
	_, ok = retryableHTTPStatusCodes[httpStatusCode]
	return ok
}
