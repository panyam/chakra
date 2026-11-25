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
// no tool call, and the turn ends. OpenAIConfig.TextToolCalls reads such a
// reply as the calls it spells, when the whole reply is calls to offered
// tools and nothing else.

// textCallIDs numbers the calls recovered from text, so each has an id of its
// own within a conversation, as the wire's tool results need.
var textCallIDs atomic.Int64

// textToolCalls reads text as tool calls, when the whole of it is one or more
// calls to the offered tools: JSON objects with a "name" and "arguments" (or
// "parameters"), each bare, inside a Markdown fence, or inside Qwen's and
// Hermes' <tool_call> tags, with only whitespace between them. names maps each
// offered tool's wire name to its real name. ok is false, and the text stays
// an answer, when anything else is in it, a name isn't offered, or the JSON
// doesn't parse.
func textToolCalls(text string, names map[string]string) (calls []ToolCall, ok bool) {
	rest := strings.TrimSpace(text)
	if rest == "" || len(names) == 0 {
		return nil, false
	}
	for rest != "" {
		var body string
		switch {
		case strings.HasPrefix(rest, "```"):
			nl := strings.IndexByte(rest, '\n')
			if nl < 0 {
				return nil, false
			}
			if lang := strings.TrimSpace(rest[3:nl]); lang != "" && lang != "json" && lang != "tool_call" {
				return nil, false
			}
			end := strings.Index(rest[nl:], "```")
			if end < 0 {
				return nil, false
			}
			body, rest = rest[nl:nl+end], rest[nl+end+3:]
		case strings.HasPrefix(rest, "<tool_call>"):
			end := strings.Index(rest, "</tool_call>")
			if end < 0 {
				return nil, false
			}
			body, rest = rest[len("<tool_call>"):end], rest[end+len("</tool_call>"):]
		case strings.HasPrefix(rest, "{"):
			body, rest = rest, ""
		default:
			return nil, false
		}
		got, ok := callObjects(body, names)
		if !ok {
			return nil, false
		}
		calls = append(calls, got...)
		rest = strings.TrimSpace(rest)
	}
	return calls, len(calls) > 0
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

// mayBeTextCall says whether text, so far, could still turn out to be a call
// written as text: nothing but whitespace yet, or a start that one can have.
func mayBeTextCall(text string) bool {
	t := strings.TrimLeft(text, " \t\r\n")
	for _, start := range []string{"```", "{", "<tool_call>"} {
		if strings.HasPrefix(t, start) || strings.HasPrefix(start, t) {
			return true
		}
	}
	return false
}

// textCallStream holds back a stream's text while it could still be calls
// written as text. Once the text can't be one, or a native tool call arrives,
// it lets everything through as it came. At the end of the stream, held text
// that is calls goes out as tool-call deltas in place of the text. Otherwise
// it goes out as text, followed by whatever was held behind it.
type textCallStream struct {
	inner   Stream
	names   map[string]string
	holding bool
	text    strings.Builder
	held    []Delta
	out     []Delta
}

func newTextCallStream(inner Stream, names map[string]string) *textCallStream {
	return &textCallStream{inner: inner, names: names, holding: true}
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
		if !s.holding {
			return d, err
		}
		if errors.Is(err, io.EOF) {
			s.holding = false
			s.release(true)
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
			if !mayBeTextCall(s.text.String()) {
				s.holding = false
				s.release(false)
			}
		case DeltaToolCallStart, DeltaToolCallArgs:
			s.holding = false
			s.release(false)
			s.out = append(s.out, d)
		default:
			s.held = append(s.held, d)
		}
	}
}

// release queues what was held: the text as tool calls when end is set and
// the text is calls, and otherwise the text as it came, then the held deltas.
// A finish reason held behind recovered calls becomes "tool_calls".
func (s *textCallStream) release(end bool) {
	text := s.text.String()
	if end {
		if calls, ok := textToolCalls(text, s.names); ok {
			for i, c := range calls {
				s.out = append(s.out, Delta{Kind: DeltaToolCallStart, Index: i, ToolCallID: c.ID, ToolName: c.Name, Text: string(c.Args.Raw())})
			}
			for _, d := range s.held {
				if d.Kind == DeltaFinish {
					d.FinishReason = "tool_calls"
				}
				s.out = append(s.out, d)
			}
			s.held = nil
			return
		}
	}
	if text != "" {
		s.out = append(s.out, Delta{Kind: DeltaText, Text: text})
	}
	s.out = append(s.out, s.held...)
	s.held = nil
}

// Close implements Stream.
func (s *textCallStream) Close() error { return s.inner.Close() }
