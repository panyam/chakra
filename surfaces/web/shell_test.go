package web

import (
	"io"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
)

// dockview-core 8's ESM build carries no CSS, so the desktop layout renders
// unstyled unless the shell links the stylesheet build.mjs extracts.
func TestShell_DockviewLayoutLinksItsStylesheet(t *testing.T) {
	css, err := fs.ReadFile(staticAssets(), "dockview.css")
	if err != nil {
		t.Fatalf("static/dockview.css is not embedded: %v", err)
	}
	if !strings.Contains(string(css), ".dv-split-view-container") {
		t.Fatalf("static/dockview.css has no split-view rules (%d bytes)", len(css))
	}

	const link = `<link rel="stylesheet" href="/static/dockview.css">`
	for _, tc := range []struct {
		query string
		want  bool
	}{
		{"/", true},
		{"/?layout=dockview", true},
		{"/?layout=mobile", false},
	} {
		rec := httptest.NewRecorder()
		shellHandler(rec, httptest.NewRequest("GET", tc.query, nil))
		body, _ := io.ReadAll(rec.Body)
		if got := strings.Contains(string(body), link); got != tc.want {
			t.Errorf("GET %s: links dockview.css = %v, want %v", tc.query, got, tc.want)
		}
	}
}
