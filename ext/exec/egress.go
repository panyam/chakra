package exec

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NetworkConfig lets sandboxed commands reach named hosts through a local
// proxy (#158), finer than AllowNetwork's all or nothing: `go mod download`
// or `npm install` against a known registry doesn't justify unrestricted
// egress. The sandbox already confines a command's network to loopback, so
// the proxy on loopback is its only way out, and the proxy decides per host.
//
// Without a sandbox (exec.Unconfined(), or Linux today) commands can ignore
// the proxy variables, so there the allowlist is advice rather than
// enforcement.
type NetworkConfig struct {
	// Allow lists the hosts commands may reach: "proxy.golang.org", or
	// "*.npmjs.org" for every subdomain (not the apex itself).
	Allow []string `json:"allow,omitempty"`
	// Ask turns a request to an unlisted host into a question for the user,
	// remembered for the session. Off, or with nobody to ask (a headless
	// run), unlisted hosts are refused.
	Ask bool `json:"ask,omitempty"`
}

// egressDial connects to an allowed host. Tests replace it to send hosts to
// local servers.
var egressDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// egress is the proxy: HTTP (CONNECT tunnels and plain requests) and SOCKS5
// on one loopback port, told apart by the first byte.
type egress struct {
	allow []string
	ask   bool
	ln    net.Listener

	mu       sync.Mutex
	decided  map[string]bool
	asker    func(ctx context.Context, question string) (bool, error)
	announce func(string)
}

func newEgress(cfg NetworkConfig) (*egress, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("exec: network proxy: %w", err)
	}
	e := &egress{ln: ln, ask: cfg.Ask, decided: map[string]bool{}}
	for _, a := range cfg.Allow {
		e.allow = append(e.allow, strings.ToLower(strings.TrimSpace(a)))
	}
	go e.serve()
	return e, nil
}

func (e *egress) addr() string { return e.ln.Addr().String() }

func (e *egress) close() error { return e.ln.Close() }

// env is what a command needs to send its traffic through the proxy.
func (e *egress) env() []string {
	httpURL := "http://" + e.addr()
	socks := "socks5h://" + e.addr()
	var out []string
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		out = append(out, k+"="+httpURL)
	}
	return append(out, "ALL_PROXY="+socks, "all_proxy="+socks, "NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1")
}

// allowed decides a host: the allowlist, then an earlier answer, then the
// user if Ask is on and someone can answer. Every decision for an unlisted
// host is remembered, so the user is asked once per host per session.
func (e *egress) allowed(ctx context.Context, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range e.allow {
		if a == host {
			return true
		}
		if strings.HasPrefix(a, "*.") {
			if ok, _ := path.Match(a, host); ok || strings.HasSuffix(host, a[1:]) {
				return true
			}
		}
	}
	e.mu.Lock()
	if v, ok := e.decided[host]; ok {
		e.mu.Unlock()
		return v
	}
	asker, announce := e.asker, e.announce
	e.mu.Unlock()

	ok := false
	if e.ask && asker != nil {
		var err error
		ok, err = asker(ctx, fmt.Sprintf("Allow commands to connect to %s for the rest of this session?", host))
		if err != nil {
			ok = false
		}
	}
	e.mu.Lock()
	e.decided[host] = ok
	e.mu.Unlock()
	if !ok && announce != nil {
		announce(fmt.Sprintf("Blocked a command's connection to %s: it isn't on the network allowlist.", host))
	}
	return ok
}

func (e *egress) serve() {
	for {
		c, err := e.ln.Accept()
		if err != nil {
			return
		}
		go e.handle(c)
	}
}

func (e *egress) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 0x05 {
		e.socks5(c, br)
		return
	}
	e.http(c, br)
}

const refusedBody = "chakra: this host isn't on the network allowlist for sandboxed commands\n"

func (e *egress) http(c net.Conn, br *bufio.Reader) {
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		host := req.URL.Hostname()
		if req.Method == http.MethodConnect {
			host = hostOnly(req.Host)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		if !e.allowed(ctx, host) {
			cancel()
			resp := &http.Response{StatusCode: http.StatusForbidden, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Content-Type": {"text/plain"}},
				Body: io.NopCloser(strings.NewReader(refusedBody)), ContentLength: int64(len(refusedBody)), Close: true}
			resp.Write(c)
			return
		}
		if req.Method == http.MethodConnect {
			up, err := egressDial(ctx, "tcp", req.Host)
			cancel()
			if err != nil {
				fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
				return
			}
			fmt.Fprintf(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
			pipe(c, br, up)
			return
		}
		// A plain-HTTP request with an absolute URL: forward it.
		req.RequestURI = ""
		tr := &http.Transport{DialContext: egressDial, Proxy: nil}
		resp, err := tr.RoundTrip(req.WithContext(ctx))
		if err != nil {
			cancel()
			fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			return
		}
		err = resp.Write(c)
		resp.Body.Close()
		cancel()
		tr.CloseIdleConnections()
		if err != nil || req.Close || resp.Close {
			return
		}
	}
}

// socks5 serves one SOCKS5 CONNECT with no authentication.
func (e *egress) socks5(c net.Conn, br *bufio.Reader) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil {
		return
	}
	if _, err := io.ReadFull(br, make([]byte, head[1])); err != nil { // offered methods
		return
	}
	c.Write([]byte{0x05, 0x00}) // no authentication
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil || req[1] != 0x01 { // CONNECT only
		c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	var host string
	switch req[3] {
	case 0x01:
		ip := make([]byte, 4)
		io.ReadFull(br, ip)
		host = net.IP(ip).String()
	case 0x04:
		ip := make([]byte, 16)
		io.ReadFull(br, ip)
		host = net.IP(ip).String()
	case 0x03:
		n, _ := br.ReadByte()
		name := make([]byte, n)
		io.ReadFull(br, name)
		host = string(name)
	default:
		return
	}
	portb := make([]byte, 2)
	if _, err := io.ReadFull(br, portb); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portb)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if !e.allowed(ctx, host) {
		c.Write([]byte{0x05, 0x02, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // not allowed by ruleset
		return
	}
	up, err := egressDial(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // connection refused
		return
	}
	c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	pipe(c, br, up)
}

// pipe copies both ways until either side closes. br holds anything the
// client sent after its request that was already buffered.
func pipe(c net.Conn, br *bufio.Reader, up net.Conn) {
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, br); closeWrite(up); done <- struct{}{} }()
	go func() { io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if tc, ok := c.(interface{ CloseWrite() error }); ok {
		tc.CloseWrite()
	}
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

var errProxyWithNetwork = errors.New("exec: Network (the egress allowlist) and AllowNetwork can't both be set: AllowNetwork already lets commands reach any host")
