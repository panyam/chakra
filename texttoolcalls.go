package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
)

// A model served without a tool-call parser for its format writes its calls
// as text. Qwen2.5-Coder on llama-server replies with a fenced JSON block,
// {"name": "query", "arguments": {...}}, and the server passes it on as the
// message's content (panyam/declaire#378). The Runner then sees an answer with
// no tool call, and the turn ends. OpenAIConfig.TextToolCalls reads the calls
// such a reply spells.
//
// Qwen writes them three ways on Declaire's questions. A reply can be the
// call alone. It can open with a sentence, "Let's correct the query", then
// the call, as every retry did. Or the call can come first, followed by an
// answer block the model wrote before the call ran, with rows it made up.
// So a call counts anywhere in a reply, in a fence or in <tool_call> tags.
// Text before the first call stays the message's text. Text after it is
// dropped, since the model wrote it without the call's result.

// textCallIDs numbers the calls recovered from text, so each has an id of its
// own within a conversation, as the wire's tool results need.
var textCallIDs atomic.Int64

// textToolCalls finds the tool calls written in text: JSON objects with a
// "name" naming an offered tool and "arguments" (or "parameters"), in a
// Markdown fence (unlabelled, json or tool_call), in Qwen's and Hermes'
// <tool_call> tags, or bare when they are the whole reply. names maps each
// offered tool's wire name to its real name. text is what goes before the
// first call. ok is false, and the reply stays as it was, when no block is
// a call to an offered tool.
func textToolCalls(reply string, names map[string]string) (calls []ToolCall, text string, ok bool) {
	if len(names) == 0 {
		return nil, reply, false
	}
	if t := strings.TrimSpace(reply); strings.HasPrefix(t, "{") {
		if calls, ok := callObjects(t, names); ok {
			return calls, "", true
		}
	}
	first := -1
	for at := 0; at < len(reply); {
		start, body, end := nextBlock(reply, at)
		if start < 0 {
			break
		}
		if got, ok := callObjects(body, names); ok {
			if first < 0 {
				first = start
			}
			calls = append(calls, got...)
		}
		at = end
	}
	if first < 0 {
		return nil, reply, false
	}
	return calls, strings.TrimSpace(reply[:first]), true
}

// nextBlock finds the first fence or <tool_call> block in reply at or after
// from that could hold a call: where it starts, what it holds, and where it
// ends. start is -1 when there is none. A fence labelled with another
// language holds no call, so it's skipped whole.
func nextBlock(reply string, from int) (start int, body string, end int) {
	for from < len(reply) {
		f := strings.Index(reply[from:], "```")
		tag := strings.Index(reply[from:], "<tool_call>")
		switch {
		case f < 0 && tag < 0:
			return -1, "", len(reply)
		case tag >= 0 && (f < 0 || tag < f):
			start = from + tag
			close := strings.Index(reply[start:], "</tool_call>")
			if close < 0 {
				return start, reply[start+len("<tool_call>"):], len(reply)
			}
			return start, reply[start+len("<tool_call>") : start+close], start + close + len("</tool_call>")
		}
		start = from + f
		nl := strings.IndexByte(reply[start:], '\n')
		if nl < 0 {
			return -1, "", len(reply)
		}
		lang := strings.TrimSpace(reply[start+3 : start+nl])
		close := strings.Index(reply[start+nl:], "```")
		if close < 0 {
			close = len(reply) - start - nl
			end = len(reply)
		} else {
			end = start + nl + close + 3
		}
		if lang == "" || lang == "json" || lang == "tool_call" {
			return start, reply[start+nl : start+nl+close], end
		}
		from = end
	}
	return -1, "", len(reply)
}

// callObjects reads body as one or more JSON call objects in a row.
func callObjects(body string, names map[string]string) ([]ToolCall, bool) {
	dec := json.NewDecoder(strings.NewReader(body))
	var calls []ToolCall
	for {
		var c struct {
			Name       string          `json:"name"`
			Arguments  json.RawMessage `json:"arguments"`
			Parameters json.RawMessage `json:"parameters"`
		}
		if err := dec.Decode(&c); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, false
		}
		real, offered := names[c.Name]
		if !offered {
			return nil, false
		}
		args := c.Arguments
		if len(args) == 0 {
			args = c.Parameters
		}
		// Some models write the arguments as a JSON string holding the object,
		// as the wire's own tool_calls do.
		var s string
		if json.Unmarshal(args, &s) == nil {
			args = json.RawMessage(s)
		}
		if t := bytes.TrimSpace(args); len(t) > 0 && t[0] != '{' {
			return nil, false
		}
		calls = append(calls, ToolCall{ID: fmt.Sprintf("text-call-%d", textCallIDs.Add(1)), Name: real, Args: toolCallArgsFromJSON(args)})
	}
	return calls, len(calls) > 0
}

// blockStarts are the text that opens a block a call can be in.
var blockStarts = []string{"```", "<tool_call>"}

// heldFrom is where text must start being held back, since a call could
// begin there: the first block start, or a tail that could still grow into
// one, or the whole of text while it's blank or opens with "{". It's
// len(text) when all of text can go out as it came.
func heldFrom(text string) int {
	if t := strings.TrimLeft(text, " \t\r\n"); t == "" || strings.HasPrefix(t, "{") {
		return 0
	}
	at := len(text)
	for _, s := range blockStarts {
		if i := strings.Index(text, s); i >= 0 && i < at {
			at = i
		}
	}
	if at < len(text) {
		return at
	}
	for _, s := range blockStarts {
		for n := min(len(s)-1, len(text)); n > 0; n-- {
			if strings.HasSuffix(text, s[:n]) {
				at = min(at, len(text)-n)
				break
			}
		}
	}
	return at
}

// textCallStream sends a reply's text as it comes, up to where a call written
// as text could start, and holds the rest, with the finish and usage, until
// the stream ends. Then the held text's calls go out as tool-call deltas, the
// text before the first of them as text, and the held deltas after, the
// finish reason as "tool_calls". With no call in it, the held text goes out
// as it came. A native tool call from the server ends the holding: from then
// on everything goes out as it came.
type textCallStream struct {
	inner  Stream
	names  map[string]string
	native bool
	sent   int // how much of text has gone out
	text   strings.Builder
	held   []Delta
	out    []Delta
}

func newTextCallStream(inner Stream, names map[string]string) *textCallStream {
	return &textCallStream{inner: inner, names: names}
}

// Recv implements Stream.
func (s *textCallStream) Recv() (Delta, error) {
	for {
		if len(s.out) > 0 {
			d := s.out[0]
			s.out = s.out[1:]
			return d, nil
		}
		d, err := s.inner.Recv()
		if s.native {
			return d, err
		}
		if errors.Is(err, io.EOF) {
			s.native = true
			s.finish()
			if len(s.out) == 0 {
				return Delta{}, io.EOF
			}
			continue
		}
		if err != nil {
			return d, err
		}
		switch d.Kind {
		case DeltaText:
			s.text.WriteString(d.Text)
			text := s.text.String()
			if at := heldFrom(text); at > s.sent {
				s.out = append(s.out, Delta{Kind: DeltaText, Text: text[s.sent:at]})
				s.sent = at
			}
		case DeltaToolCallStart, DeltaToolCallArgs:
			s.native = true
			if rest := s.text.String()[s.sent:]; rest != "" {
				s.out = append(s.out, Delta{Kind: DeltaText, Text: rest})
			}
			s.out = append(append(s.out, s.held...), d)
			s.held = nil
		default:
			s.held = append(s.held, d)
		}
	}
}

// finish queues what was held once the stream has ended.
func (s *textCallStream) finish() {
	all := s.text.String()
	rest := all[s.sent:]
	calls, before, ok := textToolCalls(all, s.names)
	if !ok {
		if rest != "" {
			s.out = append(s.out, Delta{Kind: DeltaText, Text: rest})
		}
		s.out = append(s.out, s.held...)
		return
	}
	// before is a prefix of all, trimmed; what of it hasn't gone out goes now.
	if end := strings.Index(all, before) + len(before); before != "" && end > s.sent {
		s.out = append(s.out, Delta{Kind: DeltaText, Text: all[s.sent:end]})
	}
	for i, c := range calls {
		s.out = append(s.out, Delta{Kind: DeltaToolCallStart, Index: i, ToolCallID: c.ID, ToolName: c.Name, Text: string(c.Args.Raw())})
	}
	for _, d := range s.held {
		if d.Kind == DeltaFinish {
			d.FinishReason = "tool_calls"
		}
		s.out = append(s.out, d)
	}
}

// Close implements Stream.
func (s *textCallStream) Close() error { return s.inner.Close() }
