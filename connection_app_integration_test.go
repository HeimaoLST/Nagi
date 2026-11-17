package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func openConnectionHarness(t *testing.T) *guiHarness {
	t.Helper()
	a, err := newRuntimeApp()
	if err != nil {
		t.Fatal(err)
	}
	h := &guiHarness{app: a, pump: newUIPump(t, a)}
	t.Cleanup(func() { h.shutdown(t) })
	h.pump.do(func() { a.startRuntime() })
	return h
}

func clickSettingsAction(tr *ui.Tester, action string) error {
	for i := 0; i < 12; i++ {
		if r, ok := tr.Find(action); ok && r.W > 0 && r.H > 0 && r.Y >= 0 && r.Y+r.H <= 720 {
			return tr.Click(action)
		}
		tr.Scroll(520, 360, 0, 180)
		tr.Frame()
	}
	return fmt.Errorf("connection action %q was not reachable by scrolling", action)
}

func saveAppConnection(t *testing.T, h *guiHarness, config connectionConfig) {
	t.Helper()
	h.pump.do(func() {
		h.app.openConfiguration()
		h.app.connectionForm.Draft = newConnectionSettings(&config, "").Draft
		h.app.saveConnection(config)
	})
	h.pump.wait(func() bool { return !h.app.connectionForm.Busy })
	h.pump.do(func() {
		if h.app.connectionForm.Error != "" {
			t.Error("configuration save failed: " + h.app.connectionForm.Error)
		}
	})
}
