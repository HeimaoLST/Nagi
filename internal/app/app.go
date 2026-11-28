package app

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/HeimaoLST/Nagi/assets/icons"
)

func isBundledMacApp() bool {
	executable, err := os.Executable()
	if err != nil || executable == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil && resolved != "" {
		executable = resolved
	}
	return strings.Contains(filepath.ToSlash(executable), ".app/Contents/MacOS/")
}

func Run() {
	windowOptions := mygo.WindowOptions{
		Title:         "Nagi",
		TitleBarStyle: mygo.TitleBarHiddenInset,
		Width:         1040,
		Height:        720,
		MinWidth:      720,
		MinHeight:     640,
	}
	if size := os.Getenv("NAGI_GUI_CONTENT_SIZE"); size != "" {
		widthText, heightText, ok := strings.Cut(size, "x")
		width, widthErr := strconv.Atoi(widthText)
		height, heightErr := strconv.Atoi(heightText)
		if !ok || widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
			log.Fatal("NAGI_GUI_CONTENT_SIZE must be positive integer WIDTHxHEIGHT in DIPs, for example 960x640")
		}
		windowOptions.Width, windowOptions.Height = width, height
		windowOptions.UseContentSize = true
	}
	mygo.App.SetName("Nagi")
	a, err := newProductRuntimeApp()
	if err != nil {
		a = newUnavailableApp(err)
	}
	a.requestQuit = mygo.App.Quit
	mygo.App.OnBeforeQuit(a.handleBeforeQuit)
	mygo.App.OnActivate(a.handleAppActivate)
	mygo.App.OnLocaleChanged(func(locale string) {
		a.uiLocale = locale
		a.refreshDockMenu()
	})
	mygo.App.WhenReady(func() {
		if !isBundledMacApp() {
			_ = mygo.App.Dock.SetIcon(icons.DockPNG)
		}
		windowOptions.Content = ui.View(a.nativeWindowView)
		win := mygo.NewWindow(windowOptions)
		a.win = win
		a.hideWindow, a.showWindow, a.focusWindow = win.Hide, win.Show, win.Focus
		a.focusApp, a.setDockMenu = mygo.App.Focus, mygo.App.Dock.SetMenu
		a.uiLocale = mygo.App.Locale()
		a.refreshDockMenu()
		updateContentSize := func(event string) {
			w, h := win.ContentSize()
			if w > 0 {
				a.windowWidth = w
			}
			if windowOptions.UseContentSize {
				log.Printf("nagi native-content-size event=%s unit=DIP requested_width=%d requested_height=%d actual_width=%d actual_height=%d",
					event, windowOptions.Width, windowOptions.Height, w, h)
			}
		}
		updateContentSize("created")
		win.OnResize(func() {
			updateContentSize("resize")
		})
		win.OnClose(a.handleWindowClose)
		if host := a.productHost(); host != nil {
			go a.observeProductExit(host)
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
