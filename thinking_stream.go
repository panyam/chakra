package agent

import (
	"context"
	"io"
	"strings"
)

// NewThinkingProvider wraps p to re-emit inline reasoning as DeltaReasoning
// instead of DeltaText. A model emits that reasoning in its text stream,
// delimited by openTag/closeTag, for example "<think>…</think>". The Runner
// needs nothing more. Its consumeStream already turns DeltaReasoning into
// thinking-begin/delta/end events. So a surface renders the reasoning
// distinctly with no Runner change.
//
// An empty openTag means reasoning starts at the stream head and runs until
// the first closeTag. Some "no open tag" models stream reasoning first and
// then the answer. An empty closeTag makes the hint inert. Without a
// terminator there is nothing to delimit, so p is returned unwrapped.
//
// The transform is safe at delimiter boundaries. A tag may split across two
// provider deltas, as in a "<thi" / "nk>" boundary. It is buffered and
// matched whole. The parser only reinterprets DeltaText. So native
// DeltaReasoning from a provider that already separates reasoning passes
// through untouched.
func NewThinkingProvider(p Provider, openTag, closeTag string) Provider {
	if closeTag == "" {
		return p
	}
	return &thinkingProvider{inner: p, open: openTag, close: closeTag}
}

type thinkingProvider struct {
	inner       Provider
	open, close string
}

// Stream wraps the inner stream. When DeltaText contains reasoning
// delimiters, Stream re-splits it into DeltaText / DeltaReasoning.
func (t *thinkingProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	s, err := t.inner.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &thinkingStream{inner: s, p: newThinkParser(t.open, t.close)}, nil
}

// ProviderCapabilities reports the wrapped provider's capabilities, because
// re-tagging reasoning changes nothing that is sent.
func (t *thinkingProvider) ProviderCapabilities() Capabilities { return CapabilitiesOf(t.inner) }

// Generate splits inline reasoning out of the completed Text into Reasoning,
// so the non-streaming path is consistent with Stream.
func (t *thinkingProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	resp, err := t.inner.Generate(ctx, req)
	if err != nil || resp == nil {
		return resp, err
	}
	p := newThinkParser(t.open, t.close)
	var text, reason strings.Builder
	for _, d := range append(p.feed(resp.Text), p.flush()...) {
		if d.Kind == DeltaReasoning {
			reason.WriteString(d.Text)
		} else {
			text.WriteString(d.Text)
		}
	}
	resp.Text = text.String()
	if r := reason.String(); r != "" {
		if resp.Reasoning != "" {
			resp.Reasoning += r
		} else {
			resp.Reasoning = r
		}
	}
	return resp, nil
}

// thinkingStream reinterprets an inner stream's DeltaText through a
// thinkParser, queuing the parser's output deltas so Recv can hand them out
// one at a time.
type thinkingStream struct {
	inner Stream
	p     *thinkParser
	queue []Delta
	done  bool
}

// Recv implements Stream. It pulls from the queue. When the queue is empty, it
// refills from the inner stream until a delta is available or the inner
// stream ends. It feeds DeltaText through the parser. For any non-text delta,
// it flushes the parser and then forwards the delta.
func (s *thinkingStream) Recv() (Delta, error) {
	for len(s.queue) == 0 {
		if s.done {
			return Delta{}, io.EOF
		}
		d, err := s.inner.Recv()
		if err == io.EOF {
			s.done = true
			s.queue = append(s.queue, s.p.flush()...)
			continue
		}
		if err != nil {
			return Delta{}, err
		}
		if d.Kind == DeltaText {
			s.queue = append(s.queue, s.p.feed(d.Text)...)
			continue
		}
		// A non-text delta ends the current text run. Flush any held
		// partial-tag bytes in the current mode before forwarding it, so
		// ordering is preserved.
		s.queue = append(s.queue, s.p.flush()...)
		s.queue = append(s.queue, d)
	}
	d := s.queue[0]
	s.queue = s.queue[1:]
	return d, nil
}

// Close implements Stream.
func (s *thinkingStream) Close() error { return s.inner.Close() }

// thinkParser is the stateful delimiter machine. It holds a small buffer so a
// tag straddling two feeds is matched whole, and toggles inThink each time it
// crosses a boundary. Emit mode is reasoning while inThink, text otherwise.
type thinkParser struct {
	open, close string
	inThink     bool
	buf         string
}

// newThinkParser starts inThink when openTag is empty, for models that stream
// reasoning from the head. Otherwise it starts in text mode looking for
// openTag.
func newThinkParser(open, close string) *thinkParser {
	return &thinkParser{open: open, close: close, inThink: open == ""}
}

// feed processes the next text fragment and returns the reinterpreted deltas.
func (p *thinkParser) feed(s string) []Delta {
	p.buf += s
	var out []Delta
	for {
		tag := p.open
		if p.inThink {
			tag = p.close
		}
		// There is no tag to look for, because openTag is empty and we left
		// a think block. The rest is plain text, with nothing more to split.
		if tag == "" {
			out = appendNonEmpty(out, p.emit(p.buf))
			p.buf = ""
			break
		}
		if idx := strings.Index(p.buf, tag); idx >= 0 {
			out = appendNonEmpty(out, p.emit(p.buf[:idx]))
			p.buf = p.buf[idx+len(tag):]
			p.inThink = !p.inThink
			continue
		}
		// No full tag yet. Emit everything except a suffix that could be the
		// start of the tag, and hold that suffix for the next feed.
		keep := prefixOverlap(p.buf, tag)
		out = appendNonEmpty(out, p.emit(p.buf[:len(p.buf)-keep]))
		p.buf = p.buf[len(p.buf)-keep:]
		break
	}
	return out
}

// flush emits whatever is buffered as the current mode. It runs at stream end
// or before a non-text delta, and by then buffered bytes are literal content.
func (p *thinkParser) flush() []Delta {
	if p.buf == "" {
		return nil
	}
	d := p.emit(p.buf)
	p.buf = ""
	return []Delta{d}
}

func (p *thinkParser) emit(text string) Delta {
	if p.inThink {
		return Delta{Kind: DeltaReasoning, Text: text}
	}
	return Delta{Kind: DeltaText, Text: text}
}

func appendNonEmpty(out []Delta, d Delta) []Delta {
	if d.Text == "" {
		return out
	}
	return append(out, d)
}

// prefixOverlap returns the length of the longest suffix of s that is a
// strict prefix of tag. That suffix is the partial-tag tail to hold back.
// Zero means no suffix could begin the tag.
func prefixOverlap(s, tag string) int {
	max := len(tag) - 1
	if len(s) < max {
		max = len(s)
	}
	for k := max; k > 0; k-- {
		if strings.HasPrefix(tag, s[len(s)-k:]) {
			return k
		}
	}
	return 0
}
