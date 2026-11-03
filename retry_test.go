package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// scripted serves each request with the next of statuses, then 200s.
func scripted(t *testing.T, statuses []int, header http.Header, body string) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		if i < len(statuses) {
			for k, v := range header {
				w.Header()[k] = v
			}
			w.WriteHeader(statuses[i])
			w.Write([]byte(body))
			return
		}
		w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func recordWaits(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	old := retrySleep
	retrySleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	t.Cleanup(func() { retrySleep = old })
	return &waits
}

func send(srv *httptest.Server) (*http.Response, error) {
	return sendWithRetry(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("{}"))
	})
}

func TestSendRetriesARateLimitForAsLongAsItSays(t *testing.T) {
	waits := recordWaits(t)
	srv, n := scripted(t, []int{429, 429}, nil, `{"error":{"message":"Rate limit reached ... Please try again in 5.482s.","code":"rate_limit_exceeded"}}`)
	resp, err := send(srv)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if *n != 3 || len(*waits) != 2 || (*waits)[0] != 5482*time.Millisecond {
		t.Errorf("requests %d, waits %v; want 3 requests and 5.482s waits", *n, *waits)
	}
}

func TestSendHonoursRetryAfterAndBacksOffWithout(t *testing.T) {
	waits := recordWaits(t)
	srv, _ := scripted(t, []int{503}, http.Header{"Retry-After": {"7"}}, "busy")
	if resp, err := send(srv); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	srv2, _ := scripted(t, []int{500, 502, 529}, nil, "oops")
	if resp, err := send(srv2); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	want := []time.Duration{7 * time.Second, time.Second, 2 * time.Second, 4 * time.Second}
	if len(*waits) != len(want) {
		t.Fatalf("waits %v, want %v", *waits, want)
	}
	for i := range want {
		if (*waits)[i] != want[i] {
			t.Errorf("wait %d = %v, want %v", i, (*waits)[i], want[i])
		}
	}
}

func TestSendDoesNotRetryWhatWaitingWontFix(t *testing.T) {
	recordWaits(t)
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"bad request":   {400, "bad"},
		"auth":          {401, "no"},
		"quota is gone": {429, `{"error":{"code":"insufficient_quota"}}`},
	} {
		srv, n := scripted(t, []int{c.status, c.status}, nil, c.body)
		_, err := send(srv)
		var perr *ProviderError
		if !errors.As(err, &perr) || perr.StatusCode != c.status || *n != 1 {
			t.Errorf("%s: err %v after %d requests, want one request and a ProviderError", name, err, *n)
		}
	}
}

func TestSendGivesUpAfterTheLastAttempt(t *testing.T) {
	waits := recordWaits(t)
	statuses := make([]int, maxSendAttempts+2)
	for i := range statuses {
		statuses[i] = 503
	}
	srv, n := scripted(t, statuses, http.Header{"Retry-After": {"600"}}, "down")
	_, err := send(srv)
	var perr *ProviderError
	if !errors.As(err, &perr) || perr.StatusCode != 503 || *n != maxSendAttempts {
		t.Fatalf("err %v after %d requests", err, *n)
	}
	for _, w := range *waits {
		if w != maxRetryWait {
			t.Errorf("wait %v not capped at %v", w, maxRetryWait)
		}
	}
}

func TestSendStopsWaitingWhenTheTurnIsCancelled(t *testing.T) {
	srv, n := scripted(t, []int{429, 429, 429}, http.Header{"Retry-After": {"30"}}, "slow down")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := sendWithRetry(ctx, srv.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader("{}"))
	})
	if err == nil || time.Since(start) > 5*time.Second || *n != 1 {
		t.Errorf("err %v after %v and %d requests; want it to stop waiting on cancel", err, time.Since(start), *n)
	}
}

func TestOpenAIProviderStreamsAfterARateLimit(t *testing.T) {
	recordWaits(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"message":"Please try again in 20ms."}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()
	p, err := NewOpenAIProvider(OpenAIConfig{BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(RunnerConfig{Provider: p})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}, nil)
	if err != nil || res.Text != "hello" || calls != 2 {
		t.Fatalf("text %q, err %v, calls %d", res.Text, err, calls)
	}
}
