package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"time"
)

// DefaultTimeout - default timeout is 30 seconds.
const DefaultTimeout = 30 * time.Second

var MaxAttemptError = errors.New("the maximum number of attempts was exceeded")

// Client - rest based RPC client.
type Client struct {
	httpClient          *http.Client
	httpIdleConnsCloser func()
	url                 url.URL
	newAuthToken        func() string

	random *rand.Rand
}

// CallWithContext - make a http call with context.
func (c *Client) CallWithContext(ctx context.Context, httpMethod string, path string, values url.Values, body io.Reader, length int64) (reply io.ReadCloser, err error) {
	if values != nil {
		path = path + "?" + values.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, httpMethod, c.url.String()+"/"+path, body)
	if err != nil {
		return nil, err
	}

	if c.newAuthToken != nil {
		req.Header.Set("Authorization", "Bearer "+c.newAuthToken())
	}

	if length > 0 {
		req.ContentLength = length
	}

	resp, errDo := c.httpClient.Do(req)
	if errDo != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		defer DrainBody(resp.Body)
		// Limit the ReadAll(), just in case the server responds with large data.
		b, err := ioutil.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return nil, err
		}

		if len(b) > 0 {
			return nil, errors.New(string(b))
		}

		return nil, errors.New(resp.Status)
	}

	return resp.Body, nil
}

// Call - make a REST call.
func (c *Client) Call(httpMethod string, method string, values url.Values, body io.Reader, length int64) (reply io.ReadCloser, err error) {
	ctx := context.Background()
	return c.CallWithContext(ctx, httpMethod, method, values, body, length)
}

func (c *Client) CallRetryable(ctx context.Context, httpMethod string, path string, headers http.Header, values url.Values, body io.Reader) (reply io.ReadCloser, err error) {
	// Create a done channel to control
	doneCh := make(chan struct{}, 1)

	// Indicate to our routine to exit cleanly upon return.
	defer close(doneCh)

	intent := 0
	for range newRetryTimerWithJitter(DefaultRetryUnit, DefaultRetryCap, MaxJitter, doneCh) {
		if intent >= 3 {
			return nil, MaxAttemptError
		}

		// Instantiate a new request.
		var req *http.Request

		if values != nil {
			path = path + "?" + values.Encode()
		}

		req, err = http.NewRequestWithContext(ctx, httpMethod, c.url.String()+"/"+path, body)
		if err != nil {
			return nil, err
		}

		if headers != nil {
			req.Header = headers
		}

		if c.newAuthToken != nil {
			req.Header.Set("Authorization", "Bearer "+c.newAuthToken())
		}

		var resp *http.Response

		// Initiate the request.
		resp, err = c.httpClient.Do(req)
		if err != nil {
			// For supported http requests errors verify.
			if isHTTPReqErrorRetryable(err) {
				intent++
				continue // Retry.
			}
			// For other errors, return here no need to retry.
			return nil, err
		}

		// For any known successful status, return quickly.
		for _, httpStatus := range successStatus {
			if httpStatus == resp.StatusCode {
				return resp.Body, err
			}
		}

		// Verify if rest status code is retryable.
		if isHTTPStatusRetryable(resp.StatusCode) {
			intent++
			continue // Retry.
		}

		err = fmt.Errorf("Response with status code: %d ", resp.StatusCode)
		break
	}

	return nil, err
}

// Close closes all idle connections of the underlying rest client
func (c *Client) Close() {
	if c.httpIdleConnsCloser != nil {
		c.httpIdleConnsCloser()
	}
}

func newCustomDialContext(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialer := &net.Dialer{
			Timeout:   timeout,
			KeepAlive: timeout,
		}

		return dialer.DialContext(ctx, network, addr)
	}
}

// NewClient - returns new REST client.
func NewClient(url url.URL, timeout time.Duration, newAuthToken func() string) (*Client, error) {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           newCustomDialContext(timeout),
		MaxIdleConns:          100,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	/* #nosec */
	return &Client{
		httpClient:          &http.Client{Transport: tr},
		httpIdleConnsCloser: tr.CloseIdleConnections,
		url:                 url,
		newAuthToken:        newAuthToken,
		// Introduce a new locked random seed.
		random: rand.New(&lockedRandSource{src: rand.NewSource(time.Now().UTC().UnixNano())}),
	}, nil
}
