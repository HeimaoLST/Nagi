package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--issue-tool-helper" {
		if err := runIssueToolHelperCLI(os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "issue tool helper:", err)
			os.Exit(1)
		}
		return
	}
	windowOptions := mygo.WindowOptions{
		Title:         "Nagi",
		TitleBarStyle: mygo.TitleBarHiddenInset,
		Width:         1040,
		Height:        720,
		MinWidth:      720,
		MinHeight:     640,
	}
	if size := os.Getenv("SAKI_GUI_CONTENT_SIZE"); size != "" {
		widthText, heightText, ok := strings.Cut(size, "x")
		width, widthErr := strconv.Atoi(widthText)
		height, heightErr := strconv.Atoi(heightText)
		if !ok || widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
			log.Fatal("SAKI_GUI_CONTENT_SIZE must be positive integer WIDTHxHEIGHT in DIPs, for example 960x640")
		}
		windowOptions.Width, windowOptions.Height = width, height
		windowOptions.UseContentSize = true
	}
	mygo.App.SetName("Nagi")
	a, err := newRuntimeApp()
	if err != nil {
		a = newUnavailableApp(err)
	}
	a.requestQuit = mygo.App.Quit
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if a.quitReady {
			return
		}
		e.PreventDefault()
		a.beginShutdown()
	})
	mygo.App.WhenReady(func() {
		windowOptions.Content = ui.View(a.view)
		win := mygo.NewWindow(windowOptions)
		a.win = win
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
		win.OnClose(func(e *mygo.CloseEvent) {
			if a.quitReady {
				return
			}
			e.PreventDefault()
			a.beginShutdown()
		})
		a.startRuntime()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func newApp() (*appModel, error) {
	a := seededApp()
	a.demo = true
	return a, nil
}
