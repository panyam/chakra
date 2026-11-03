package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// Embedding is a dense vector produced by an Embedder. It is a defined type,
// not a bare []float32, so the comparison is a method on it and signatures
// read in domain terms. Every Embedding from one Embedder has the same
// dimensionality. Comparing embeddings from different Embedders is
// meaningless.
type Embedding []float32

// Cosine returns the cosine of the angle between a and b, in [-1, 1], where 1
// means the same direction. It returns 0 when either vector is zero or the
// lengths differ. A degenerate comparison ranks last instead of erroring, so
// a recall query never fails on a malformed vector.
func (a Embedding) Cosine(b Embedding) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Embedder turns text into Embeddings, so memory can be recalled by semantic
// similarity instead of substring match. It is the sibling of Provider. It is
// an independently swappable seam, such as a hosted API, a local model, or a
// stub. The rest of the agent depends on it only through this interface.
//
// Embed returns exactly one Embedding per input text, in the same order. A
// store must be built and queried with the same Embedder.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([]Embedding, error)
}

// OpenAIEmbedderConfig configures an OpenAIEmbedder.
type OpenAIEmbedderConfig struct {
	// BaseURL is the API root including any version prefix, e.g.
	// "http://localhost:1234/v1". The embedder appends "/embeddings".
	BaseURL string

	// APIKey, when non-empty, is sent as a Bearer token. Local servers
	// commonly need none.
	APIKey string

	// Model is the embedding model identifier sent on every request.
	Model string

	// HTTPClient overrides http.DefaultClient.
	HTTPClient *http.Client

	// TracerProvider opts the embedder into an agent.embed span per call,
	// with input count and model attributes. Nil or NoopTracerProvider
	// means zero overhead, the repo-wide pattern.
	TracerProvider core.TracerProvider
}

// OpenAIEmbedder is an Embedder over any OpenAI-compatible /embeddings
// endpoint (OpenAI, LM Studio, Ollama, ...). It uses net/http directly with
// no SDK dependency, the same approach as OpenAIProvider.
type OpenAIEmbedder struct {
	cfg  OpenAIEmbedderConfig
	http *http.Client
	tp   core.TracerProvider
}

// NewOpenAIEmbedder validates cfg and returns the embedder. BaseURL and
// Model are required.
func NewOpenAIEmbedder(cfg OpenAIEmbedderConfig) (*OpenAIEmbedder, error) {
	if cfg.BaseURL == "" || cfg.Model == "" {
		return nil, fmt.Errorf("agent: OpenAIEmbedderConfig requires BaseURL and Model")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	tp := cfg.TracerProvider
	if tp == nil {
		tp = core.NoopTracerProvider{}
	}
	return &OpenAIEmbedder{cfg: cfg, http: hc, tp: tp}, nil
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding Embedding `json:"embedding"`
	} `json:"data"`
}

// Embed implements Embedder. It sends all texts in one request. The API
// echoes an index per embedding, and the indexes need not be in request
// order. So Embed reorders the embeddings to match the input order.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([]Embedding, error) {
	ctx, span := e.tp.StartSpan(ctx, "agent.embed",
		core.Attribute{Key: "agent.embed.count", Value: fmt.Sprint(len(texts))},
		core.Attribute{Key: "agent.embed.model", Value: e.cfg.Model})
	defer span.End()

	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embeddingRequest{Model: e.cfg.Model, Input: texts})
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("agent: encode embedding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	}

	resp, err := e.http.Do(req)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := new(bytes.Buffer)
		_, _ = msg.ReadFrom(resp.Body)
		err := &ProviderError{StatusCode: resp.StatusCode, Body: msg.String()}
		span.RecordError(err)
		return nil, err
	}

	var out embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("agent: decode embedding response: %w", err)
	}
	if len(out.Data) != len(texts) {
		err := fmt.Errorf("agent: embedding count mismatch: got %d for %d inputs", len(out.Data), len(texts))
		span.RecordError(err)
		return nil, err
	}
	vecs := make([]Embedding, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			continue
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}

// DefaultStubEmbedderDim is the vector width StubEmbedder uses when its Dim
// is unset.
const DefaultStubEmbedderDim = 64

// StubEmbedder is a deterministic, dependency-free Embedder for tests. It
// projects each text into a fixed-width bag-of-words vector, and hashes each
// lowercased token into a bucket. So texts that share words produce similar
// vectors, and Embedding.Cosine is meaningful. It needs no model and no
// network, and it is stable across runs. It is the embedding counterpart of
// StubProvider.
type StubEmbedder struct {
	// Dim is the vector width. Zero uses DefaultStubEmbedderDim.
	Dim int
}

// Embed implements Embedder.
func (e StubEmbedder) Embed(ctx context.Context, texts []string) ([]Embedding, error) {
	dim := e.Dim
	if dim <= 0 {
		dim = DefaultStubEmbedderDim
	}
	out := make([]Embedding, len(texts))
	for i, t := range texts {
		vec := make(Embedding, dim)
		for _, tok := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(tok))
			vec[h.Sum32()%uint32(dim)]++
		}
		out[i] = vec
	}
	return out, nil
}
