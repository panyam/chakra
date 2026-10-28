package web

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http/httptest"
	"regexp"
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

// The frontend's IslandPage mounts whatever #page-spec names, into the
// [data-slot] it names. Each name must be a key of the registry in
// web/src/page.ts, and each slot must be on the page.
func TestShell_PageSpecNamesEachLayoutsIslands(t *testing.T) {
	specRE := regexp.MustCompile(`(?s)<script type="application/json" id="page-spec">(.*?)</script>`)
	for _, tc := range []struct {
		query   string
		islands []string
	}{
		{"/", []string{"status", "dock"}},
		{"/?layout=mobile", []string{"status", "mobile"}},
	} {
		rec := httptest.NewRecorder()
		shellHandler(rec, httptest.NewRequest("GET", tc.query, nil))
		body := rec.Body.String()
		m := specRE.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("GET %s: no #page-spec script", tc.query)
		}
		var spec pageSpec
		if err := json.Unmarshal([]byte(m[1]), &spec); err != nil {
			t.Fatalf("GET %s: page spec is not JSON: %v\n%s", tc.query, err, m[1])
		}
		var names []string
		for _, is := range spec.Islands {
			names = append(names, is.Name)
			if !strings.Contains(body, `data-slot="`+is.Slot+`"`) {
				t.Errorf("GET %s: island %q mounts into slot %q, which is not on the page", tc.query, is.Name, is.Slot)
			}
		}
		if strings.Join(names, ",") != strings.Join(tc.islands, ",") {
			t.Errorf("GET %s: islands %v, want %v", tc.query, names, tc.islands)
		}
	}
}

// A phone with no ?layout= gets the mobile layout, and ?layout= overrides the
// user agent either way, so the layout select and the More drawer's "Desktop
// layout" link work on any device.
func TestShell_PickLayout(t *testing.T) {
	const iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	const android = "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Mobile Safari/537.36"
	const mac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"
	for _, tc := range []struct {
		query, ua, want string
	}{
		{"/", iphone, "mobile"},
		{"/", android, "mobile"},
		{"/", mac, "dockview"},
		{"/?layout=dockview", iphone, "dockview"},
		{"/?layout=mobile", mac, "mobile"},
	} {
		req := httptest.NewRequest("GET", tc.query, nil)
		req.Header.Set("User-Agent", tc.ua)
		if got := pickLayout(req); got != tc.want {
			t.Errorf("GET %s from %.30s…: layout %q, want %q", tc.query, tc.ua, got, tc.want)
		}
	}
}
