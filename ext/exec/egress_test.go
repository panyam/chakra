package exec

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeHosts sends the named hosts to local servers, so the proxy's decisions
// can be tested without the network.
func fakeHosts(t *testing.T, hosts map[string]string) {
	t.Helper()
	old := egressDial
	egressDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, _, _ := net.SplitHostPort(addr)
		if target, ok := hosts[h]; ok {
			addr = target
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	t.Cleanup(func() { egressDial = old })
}

func okServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	t.Cleanup(srv.Close)
	return srv
}

func startEgress(t *testing.T, cfg NetworkConfig) *egress {
	t.Helper()
	e, err := newEgress(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.close() })
	return e
}

func viaProxy(t *testing.T, e *egress, target string) (int, string) {
	t.Helper()
	proxyURL, _ := url.Parse("http://" + e.addr())
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := c.Get(target)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestEgressAllowsListedHostsAndRefusesOthers(t *testing.T) {
	ok := okServer(t, "hello from allowed")
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tls ok") }))
	defer tlsSrv.Close()
	fakeHosts(t, map[string]string{
		"allowed.test":  ok.Listener.Addr().String(),
		"sub.wild.test": ok.Listener.Addr().String(),
		"secure.test":   tlsSrv.Listener.Addr().String(),
		"blocked.test":  ok.Listener.Addr().String(),
		"wild.test":     ok.Listener.Addr().String(),
	})
	e := startEgress(t, NetworkConfig{Allow: []string{"allowed.test", "secure.test", "*.wild.test"}})
	var announced []string
	e.announce = func(s string) { announced = append(announced, s) }

	if code, body := viaProxy(t, e, "http://allowed.test/x"); code != 200 || body != "hello from allowed" {
		t.Errorf("plain http to an allowed host: %d %q", code, body)
	}
	if code, body := viaProxy(t, e, "https://secure.test/"); code != 200 || body != "tls ok" {
		t.Errorf("https (CONNECT) to an allowed host: %d %q", code, body)
	}
	if code, _ := viaProxy(t, e, "http://sub.wild.test/"); code != 200 {
		t.Errorf("a subdomain of a wildcard: %d", code)
	}
	if code, body := viaProxy(t, e, "http://blocked.test/"); code != 403 || !strings.Contains(body, "allowlist") {
		t.Errorf("an unlisted host: %d %q", code, body)
	}
	if code, _ := viaProxy(t, e, "http://wild.test/"); code != 403 {
		t.Errorf("a wildcard's apex should not match: %d", code)
	}
	if _, err := (&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: e.addr()})}}).Get("https://blocked.test/"); err == nil {
		t.Error("CONNECT to an unlisted host went through")
	}
	if len(announced) == 0 || !strings.Contains(announced[0], "blocked.test") {
		t.Errorf("no block announced: %q", announced)
	}
}

func TestEgressAsksOnceAndRemembers(t *testing.T) {
	ok := okServer(t, "ok")
	fakeHosts(t, map[string]string{"yes.test": ok.Listener.Addr().String(), "no.test": ok.Listener.Addr().String()})
	e := startEgress(t, NetworkConfig{Ask: true})
	var asked atomic.Int32
	e.asker = func(_ context.Context, q string) (bool, error) {
		asked.Add(1)
		return strings.Contains(q, "yes.test"), nil
	}
	for i := 0; i < 2; i++ {
		if code, _ := viaProxy(t, e, "http://yes.test/"); code != 200 {
			t.Errorf("approved host, request %d: %d", i, code)
		}
		if code, _ := viaProxy(t, e, "http://no.test/"); code != 403 {
			t.Errorf("declined host, request %d: %d", i, code)
		}
	}
	if n := asked.Load(); n != 2 {
		t.Errorf("asked %d times, want once per host", n)
	}

	quiet := startEgress(t, NetworkConfig{})
	quiet.asker = func(context.Context, string) (bool, error) { t.Error("asked with Ask off"); return true, nil }
	if code, _ := viaProxy(t, quiet, "http://yes.test/"); code != 403 {
		t.Errorf("with Ask off an unlisted host passed: %d", code)
	}
}

// socksGet fetches path from host through a SOCKS5 proxy with a minimal
// client, returning the reply code and, when it succeeded, the response.
func socksGet(t *testing.T, proxy, host string) (byte, string) {
	t.Helper()
	c, err := net.Dial("tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte{0x05, 0x01, 0x00})
	buf := make([]byte, 2)
	io.ReadFull(c, buf)
	req := append([]byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}, host...)
	c.Write(append(req, 0, 80))
	reply := make([]byte, 10)
	if _, err := io.ReadFull(c, reply); err != nil {
		return 0xff, ""
	}
	if reply[1] != 0 {
		return reply[1], ""
	}
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return 0, err.Error()
	}
	b, _ := io.ReadAll(resp.Body)
	return 0, string(b)
}

func TestEgressSpeaksSOCKS5(t *testing.T) {
	ok := okServer(t, "socks ok")
	fakeHosts(t, map[string]string{"allowed.test": ok.Listener.Addr().String(), "blocked.test": ok.Listener.Addr().String()})
	e := startEgress(t, NetworkConfig{Allow: []string{"allowed.test"}})
	if code, body := socksGet(t, e.addr(), "allowed.test"); code != 0 || body != "socks ok" {
		t.Errorf("allowed: reply %d, %q", code, body)
	}
	if code, _ := socksGet(t, e.addr(), "blocked.test"); code != 0x02 {
		t.Errorf("blocked: reply %d, want 2 (not allowed by ruleset)", code)
	}
}

func TestCommandsGetTheProxyVariables(t *testing.T) {
	src := mustSource(t, Config{
		Roots:    []string{workspace(t)},
		Commands: []CommandSpec{{Name: "env", Argv: helperArgv(t, "getenv", "HTTPS_PROXY"), Description: "Print a variable.", ReadOnly: true}},
		Network:  &NetworkConfig{Allow: []string{"proxy.golang.org"}},
		Sandbox:  Unconfined(),
	})
	defer src.Close()
	out := call(t, src, "run_env", nil)
	if !strings.Contains(out, "HTTPS_PROXY=http://"+src.egress.addr()) {
		t.Errorf("command didn't get the proxy: %q", out)
	}
	if _, err := NewSource(Config{Roots: []string{workspace(t)}, Shell: &ShellConfig{AllowNetwork: true}, Network: &NetworkConfig{}, Sandbox: Unconfined()}); err == nil {
		t.Error("Network and AllowNetwork together were accepted")
	}
}

func TestLongRunningProcessesGetTheProxyToo(t *testing.T) {
	src := mustSource(t, Config{
		Roots:   []string{workspace(t)},
		Shell:   &ShellConfig{Processes: &ProcessConfig{}},
		Network: &NetworkConfig{Allow: []string{"proxy.golang.org"}},
		Sandbox: recordingSandbox{new(int)},
	})
	defer src.Close()
	out, _ := callProc(t, src, ExecCommandTool, map[string]any{"cmd": "echo proxy=$HTTPS_PROXY", "yield_ms": 3000})
	if !strings.Contains(out, "proxy=http://"+src.egress.addr()) {
		t.Errorf("exec_command didn't get the proxy: %q", out)
	}
}
