package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

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
