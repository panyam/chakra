package host

import (
	"errors"
	"fmt"
)

// ErrNoProcessControl reports that no extension called source can be driven
// from a surface. Its processes can still be read, but not resized or typed
// into.
var ErrNoProcessControl = errors.New("host: no process control for that source")

// ResizeProcess resizes process id of the extension named source, which
// ProcessOutput.Source names. It returns ErrNoProcessControl when that
// extension doesn't implement ProcessController.
func (a *App) ResizeProcess(source, id string, cols, rows int) error {
	pc, err := a.processController(source)
	if err != nil {
		return err
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("host: a terminal size needs positive cols and rows, not %dx%d", cols, rows)
	}
	return pc.ResizeProcess(id, cols, rows)
}

// WriteProcess sends data to process id of the extension named source. It
// comes from the user, so it bypasses the approval gate (see
// ProcessController). It returns ErrNoProcessControl when that extension
// doesn't implement ProcessController.
func (a *App) WriteProcess(source, id, data string) error {
	pc, err := a.processController(source)
	if err != nil {
		return err
	}
	return pc.WriteProcess(id, data)
}

func (a *App) processController(source string) (ProcessController, error) {
	for _, ext := range a.extensions {
		if ext.Name() != source {
			continue
		}
		if pc, ok := ext.(ProcessController); ok {
			return pc, nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrNoProcessControl, source)
}
