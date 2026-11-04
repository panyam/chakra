package host

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/panyam/chakra"
)

type controlledExt struct {
	BaseExtension
	name    string
	resized []int
	written []string
}

func (e *controlledExt) Name() string { return e.name }
func (e *controlledExt) ResizeProcess(id string, cols, rows int) error {
	if id != "p1" {
		return errors.New("no such process")
	}
	e.resized = append(e.resized, cols, rows)
	return nil
}
func (e *controlledExt) WriteProcess(id, data string) error {
	e.written = append(e.written, id+":"+data)
	return nil
}

func TestAppRoutesProcessControlBySource(t *testing.T) {
	ctl := &controlledExt{name: "exec"}
	plain := &classifierExt{name: "other"}
	cfg := &Config{Model: ModelConfig{BaseURL: "http://stub", Model: "stub"}}
	app, err := NewApp(cfg, io.Discard, strings.NewReader(""), WithProvider(agent.NewStubProvider()), WithExtension(ctl, plain))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.ResizeProcess("exec", "p1", 90, 20); err != nil {
		t.Fatal(err)
	}
	if err := app.WriteProcess("exec", "p1", "q"); err != nil {
		t.Fatal(err)
	}
	if len(ctl.resized) != 2 || ctl.resized[0] != 90 || ctl.resized[1] != 20 || len(ctl.written) != 1 || ctl.written[0] != "p1:q" {
		t.Errorf("resized %v written %v", ctl.resized, ctl.written)
	}
	if err := app.ResizeProcess("exec", "p9", 90, 20); err == nil {
		t.Error("the extension's error was dropped")
	}
	if err := app.ResizeProcess("exec", "p1", 0, 20); err == nil {
		t.Error("a zero size was passed on")
	}
	for _, src := range []string{"other", "missing"} {
		if err := app.WriteProcess(src, "p1", "q"); !errors.Is(err, ErrNoProcessControl) {
			t.Errorf("%s: err = %v, want ErrNoProcessControl", src, err)
		}
	}
}
