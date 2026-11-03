package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// FileToolResultStore is a filesystem-backed ToolResultStore. It writes one
// JSON file per ref under a directory. It lives here in agent/ and not in a
// sibling module, because it uses only the stdlib. The sibling modules
// (agent/store/redis, agent/store/gorm) exist to isolate heavy database
// dependencies, and this store has none.
//
// It is the natural store for a local or coding agent. There is no server to
// run, and it is durable across restarts at no extra cost. The blobs are
// plain files, so the agent can read them with the file tools it already has.
// That is the "filesystem as externalized memory" pattern. Each blob is one
// immutable file per ref, so forks share blobs by path with no copy.
// Retention is just deleting old files.
type FileToolResultStore struct {
	dir string
}

var _ ToolResultStore = (*FileToolResultStore)(nil)

// NewFileToolResultStore returns a store that writes under dir. It creates
// dir and its parents if they are absent. The store owns the directory.
// Point two stores at the same dir only if you intend them to share blobs.
func NewFileToolResultStore(dir string) (*FileToolResultStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("agent: FileToolResultStore requires a directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("agent: creating tool-result dir %q: %w", dir, err)
	}
	return &FileToolResultStore{dir: dir}, nil
}

// refToFilename maps a ref to a unique, cross-platform-safe filename.
// Safe bytes pass through. Every other byte becomes "_XX" hex. That
// includes the ':' in a "res:..." ref and the escape byte itself. The
// escaping is injective, so distinct refs never collide on one file.
func refToFilename(ref string) string {
	var b strings.Builder
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "_%02x", c)
		}
	}
	b.WriteString(".json")
	return b.String()
}

func (s *FileToolResultStore) path(ref string) string {
	return filepath.Join(s.dir, refToFilename(ref))
}

// PutToolResult implements ToolResultStore. The blob is written to a temp
// file and renamed into place. Rename is atomic within one filesystem. So a
// crash mid-write never leaves a partial blob for a later read to trip on.
func (s *FileToolResultStore) PutToolResult(ctx context.Context, req PutToolResultRequest) (PutToolResultResponse, error) {
	body, err := json.Marshal(req.Result)
	if err != nil {
		return PutToolResultResponse{}, fmt.Errorf("agent: encoding tool result: %w", err)
	}
	final := s.path(req.Ref)
	tmp, err := os.CreateTemp(s.dir, "put-*.tmp")
	if err != nil {
		return PutToolResultResponse{}, fmt.Errorf("agent: staging tool result: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return PutToolResultResponse{}, fmt.Errorf("agent: writing tool result: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return PutToolResultResponse{}, fmt.Errorf("agent: closing tool result: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return PutToolResultResponse{}, fmt.Errorf("agent: committing tool result: %w", err)
	}
	return PutToolResultResponse{}, nil
}

// GetToolResult implements ToolResultStore. A file may be missing because
// it was never stored or was deleted. That gives Found=false, not an error.
func (s *FileToolResultStore) GetToolResult(ctx context.Context, req GetToolResultRequest) (GetToolResultResponse, error) {
	body, err := os.ReadFile(s.path(req.Ref))
	if os.IsNotExist(err) {
		return GetToolResultResponse{}, nil
	}
	if err != nil {
		return GetToolResultResponse{}, err
	}
	var result core.ToolResult
	if err := json.Unmarshal(body, &result); err != nil {
		return GetToolResultResponse{}, fmt.Errorf("agent: corrupt tool result %q: %w", req.Ref, err)
	}
	return GetToolResultResponse{Result: result, Found: true}, nil
}
