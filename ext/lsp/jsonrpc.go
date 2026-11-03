package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// errServerGone fails an in-flight request when the read loop ends before the
// reply arrives.
//
// A language server that crashes leaves every waiter with nothing to wait for.
// The crash itself does little harm, and the hang does the damage. A turn that
// blocks forever on a dead subprocess looks like a slow model. Ending the read
// loop fails every pending call at once, so the caller finds out on the next
// request instead of on the context deadline.
var errServerGone = errors.New("lsp: server exited")

// message is one JSON-RPC frame in either direction.
//
// It is one struct, not separate request and response types, because the
// fields present decide what a frame is. A frame with an ID and a Method is a
// request from the server. A frame with an ID and no Method is a reply to
// ours, and a frame with no ID is a notification. With split types we would
// have to pick one to decode into before reading the fields that decide it.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("lsp: rpc error %d: %s", e.Code, e.Message) }

// defaultCallTimeout bounds a single request to the server.
//
// It exists because nothing else does. A server that accepts a request, never
// answers it, and keeps its stdout open gives the read loop nothing to fail
// on, so the loop never exits, c.done never closes, and a caller waits on its
// own context. The navigation tools pass the Runner's context straight down,
// and that context often has no deadline, so a single wedged server could
// hang a turn with no error anywhere to explain it.
//
// It is generous. Every request under it is a local IPC round trip, and a
// healthy server answers in milliseconds. It is a backstop against a hang, not
// a latency budget.
const defaultCallTimeout = 30 * time.Second

// conn is a JSON-RPC 2.0 connection over the LSP base protocol: a
// Content-Length header, a blank line, then the JSON body.
//
// The server talks back on the same pipe for three reasons. It replies to our
// requests, sends notifications we asked for (publishDiagnostics), and makes
// requests of its own that we must answer. So the read loop demultiplexes
// instead of assuming replies arrive in order.
type conn struct {
	w io.WriteCloser
	r *bufio.Reader

	// writeMu serializes frames. Two goroutines interleaving a header and a
	// body would produce a stream neither side can resynchronize.
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan *message

	// onNotify handles server notifications. Set once before the read loop
	// starts, never afterwards, so it needs no lock.
	onNotify func(method string, params json.RawMessage)

	// done closes when the read loop ends. A pending call has no other way
	// to learn the server is gone.
	done     chan struct{}
	doneOnce sync.Once

	// callTimeout bounds every request. It is per-conn rather than a package
	// var, so a test can shorten it without reaching into global state
	// (constraint C3 in mcpkit's CONSTRAINTS.md).
	callTimeout time.Duration
}

func newConn(w io.WriteCloser, r io.Reader) *conn {
	return &conn{
		w:           w,
		r:           bufio.NewReader(r),
		pending:     map[int64]chan *message{},
		done:        make(chan struct{}),
		callTimeout: defaultCallTimeout,
	}
}

// run reads frames until the stream ends, then fails every pending call.
func (c *conn) run() {
	defer c.shutdown()
	for {
		frame, err := readFrame(c.r)
		if err != nil {
			return
		}
		var msg message
		if err := json.Unmarshal(frame, &msg); err != nil {
			// A frame we cannot parse is the server's problem. It is no reason
			// to tear down a working connection, so skip it and keep reading.
			continue
		}
		switch {
		case msg.ID != nil && msg.Method != "":
			c.answer(&msg)
		case msg.ID != nil:
			c.deliver(&msg)
		case msg.Method != "" && c.onNotify != nil:
			c.onNotify(msg.Method, msg.Params)
		}
	}
}

// answer replies to a request the server made of us.
//
// Every server request gets a reply, including the ones we have no
// implementation for. A server that is waiting on us has stopped producing
// diagnostics. gopls issues workspace/configuration during initialization and
// blocks on the answer. If we stayed silent, that would look like a hang with
// no error anywhere to explain it.
func (c *conn) answer(req *message) {
	reply := &message{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "workspace/configuration":
		// One settings object per requested item, each empty. We override
		// nothing and let the server keep its defaults. A single object, or
		// null, is a length mismatch, and the server treats it as a protocol
		// error.
		var params struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(req.Params, &params)
		out := make([]map[string]any, len(params.Items))
		for i := range out {
			out[i] = map[string]any{}
		}
		reply.Result, _ = json.Marshal(out)
	default:
		reply.Result = json.RawMessage("null")
	}
	_ = c.send(reply)
}

func (c *conn) deliver(msg *message) {
	c.mu.Lock()
	ch, ok := c.pending[*msg.ID]
	delete(c.pending, *msg.ID)
	c.mu.Unlock()
	if ok {
		ch <- msg
	}
}

func (c *conn) shutdown() {
	c.doneOnce.Do(func() { close(c.done) })
	c.mu.Lock()
	pending := c.pending
	c.pending = map[int64]chan *message{}
	c.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
}

// call sends a request and waits for its reply, decoding it into result.
//
// Returns errServerGone if the connection ends first, and the context's error
// if the caller gives up. A cancelled call drops its pending entry, so a late
// reply is discarded rather than delivered to a receiver nobody is reading.
func (c *conn) call(ctx context.Context, method string, params, result any) error {
	// The call is always bounded, and the bound never extends a caller's
	// deadline. A caller with a shorter deadline keeps it, and a caller with
	// none gets this one.
	ctx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan *message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.send(&message{JSONRPC: "2.0", ID: &id, Method: method, Params: mustRaw(params)}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.done:
		return errServerGone
	case msg, ok := <-ch:
		if !ok {
			return errServerGone
		}
		if msg.Error != nil {
			return msg.Error
		}
		if result == nil || len(msg.Result) == 0 {
			return nil
		}
		return json.Unmarshal(msg.Result, result)
	}
}

// notify sends a request that expects no reply.
func (c *conn) notify(method string, params any) error {
	return c.send(&message{JSONRPC: "2.0", Method: method, Params: mustRaw(params)})
}

func (c *conn) send(msg *message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.w.Write(body)
	return err
}

// mustRaw marshals params, falling back to null. An unmarshalable params value
// is a programming error in this package rather than something a caller can
// cause, and failing the send would report it at the wrong layer.
func mustRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// readFrame reads one Content-Length delimited message.
//
// Headers other than Content-Length are skipped rather than rejected. The base
// protocol also defines Content-Type, and a server is free to send headers we
// have never heard of. Refusing them would break on a spec revision, and
// ignoring them costs us nothing.
func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("lsp: malformed header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("lsp: bad Content-Length %q: %w", value, err)
			}
		}
	}
	if length < 0 {
		return nil, errors.New("lsp: frame has no Content-Length")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
