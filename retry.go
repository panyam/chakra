package agent

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Transient HTTP failures from a model endpoint are retried before they
// become a turn error: a 429 from a tokens-per-minute limit, an overloaded or
// briefly unavailable server. The endpoint usually says how long to wait, and
// waiting that long is what every vendor's own client does, so a turn doesn't
// end over a five-second limit window (#203).
const (
	// maxSendAttempts bounds one request: the first try plus five retries.
	maxSendAttempts = 6
	// maxRetryWait caps a single wait, whatever the endpoint asks for.
	maxRetryWait = 60 * time.Second
)

// retrySleep waits d or until ctx ends. Tests replace it.
var retrySleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendWithRetry sends the request newReq builds, building a fresh one per
// attempt so the body can be read again. A 2xx response is returned open for
// the caller to read. A retryable status (429 other than an exhausted quota,
// 500, 502, 503, 504, 529) is retried after the wait the endpoint asks for,
// or an exponential backoff when it doesn't say; any other status, or the
// last attempt's, comes back as a *ProviderError. A transport error is
// returned as is, since the request may have reached the server.
func sendWithRetry(ctx context.Context, client *http.Client, newReq func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		perr := &ProviderError{StatusCode: resp.StatusCode, Body: string(msg)}
		if attempt >= maxSendAttempts || !retryable(resp.StatusCode, perr.Body) {
			return nil, perr
		}
		if err := retrySleep(ctx, retryWait(resp.Header, perr.Body, attempt)); err != nil {
			return nil, perr
		}
	}
}

func retryable(status int, body string) bool {
	switch status {
	case http.StatusTooManyRequests:
		// An exhausted quota or billing limit won't clear by waiting.
		return !strings.Contains(body, "insufficient_quota")
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// tryAgainIn matches the wait OpenAI writes into a rate-limit message, such
// as "Please try again in 5.482s" or "in 820ms".
var tryAgainIn = regexp.MustCompile(`(?i)try again in ([0-9.]+)\s*(ms|s)\b`)

// retryWait is how long to wait before attempt+1: the Retry-After or
// retry-after-ms header, then a wait named in the body, then 1s, 2s, 4s and so
// on, capped at maxRetryWait.
func retryWait(h http.Header, body string, attempt int) time.Duration {
	wait := time.Duration(0)
	if ms, err := strconv.ParseFloat(h.Get("retry-after-ms"), 64); err == nil && ms > 0 {
		wait = time.Duration(ms * float64(time.Millisecond))
	} else if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil {
			wait = time.Duration(secs * float64(time.Second))
		} else if at, err := http.ParseTime(v); err == nil {
			wait = time.Until(at)
		}
	} else if m := tryAgainIn.FindStringSubmatch(body); m != nil {
		if n, err := strconv.ParseFloat(m[1], 64); err == nil {
			unit := time.Second
			if strings.EqualFold(m[2], "ms") {
				unit = time.Millisecond
			}
			wait = time.Duration(n * float64(unit))
		}
	}
	if wait <= 0 {
		wait = time.Second << (attempt - 1)
	}
	if wait > maxRetryWait {
		wait = maxRetryWait
	}
	return wait
}
