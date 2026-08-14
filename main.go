package main

import (
	"embed"
	"log"
	"os"
	"strings"

	"github.com/kangzyz/KSpeech/internal/assistant"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

var (
	version = "dev"
	commit  = ""
)

//go:embed all:frontend/dist
var frontendAssets embed.FS

// Wails passes these to CreateIconFromResourceEx, which understands a PNG but
// not a complete .ico file. The window icon itself comes from the executable's
// resource ID 3 (rsrc_windows_amd64.syso); these are the runtime fallbacks.
//
//go:embed assets/kspeech-app.png
var appIcon []byte

//go:embed assets/kspeech-tray.png
var trayIcon []byte

func main() {
	service, err := NewDesktopService(BuildInfo{Version: version, Commit: commit})
	if err != nil {
		log.Fatal(err)
	}

	application.RegisterEvent[AppSnapshot](stateEvent)
	application.RegisterEvent[LiveState](liveStateEvent)
	application.RegisterEvent[UINotification](notificationEvent)
	application.RegisterEvent[assistant.State](assistantEvent)
	notificationService := notifications.New()

	app := application.New(application.Options{
		Name:        "KSpeech",
		Description: "本地实时语音字幕",
		Icon:        appIcon,
		Services: []application.Service{
			application.NewService(notificationService),
			application.NewService(service),
		},
		Assets: application.AssetOptions{
			Handler:        application.BundledAssetFileServer(frontendAssets),
			DisableLogging: true,
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
			AdditionalBrowserArgs:         debugBrowserArgs(),
		},
	})

	consoleX, consoleY, consoleWidth, consoleHeight, positioned := service.consoleBounds()
	consoleOptions := application.WebviewWindowOptions{
		Name:               consoleWindowName,
		Title:              "KSpeech",
		Width:              consoleWidth,
		Height:             consoleHeight,
		MinWidth:           minConsoleWidth,
		MinHeight:          minConsoleHeight,
		URL:                "/?view=console",
		Frameless:          true,
		AlwaysOnTop:        true,
		BackgroundType:     application.BackgroundTypeTransparent,
		BackgroundColour:   application.NewRGBA(0, 0, 0, 0),
		DevToolsEnabled:    false,
		ZoomControlEnabled: false,
		Windows: application.WindowsWindow{
			HiddenOnTaskbar:                   true,
			DisableFramelessWindowDecorations: true,
			WindowDidMoveDebounceMS:           250,
			NonClientRegionSupport:            true,
		},
	}
	if positioned {
		consoleOptions.InitialPosition = application.WindowXY
		consoleOptions.X = consoleX
		consoleOptions.Y = consoleY
	}
	// A frameless window has no system frame to drag, so Wails resizes it from
	// the frontend instead: the runtime watches the outer few pixels and asks
	// the window to resize. That needs WS_THICKFRAME, which SetResizable owns,
	// so locking the window is what turns edge dragging off.
	consoleWindow := app.Window.NewWithOptions(consoleOptions)

	settingsWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      settingsWindowName,
		Title:     "KSpeech 设置",
		Width:     960,
		Height:    700,
		MinWidth:  760,
		MinHeight: 560,
		URL:       "/?view=settings",
		Hidden:    true,
		Windows:   application.WindowsWindow{DisableMenu: true},
	})

	settingsWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		settingsWindow.Hide()
		event.Cancel()
	})
	consoleWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		consoleWindow.Hide()
		event.Cancel()
	})
	consoleWindow.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) {
		service.scheduleConsolePersistence()
	})
	consoleWindow.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) {
		service.scheduleConsolePersistence()
	})

	tray := app.SystemTray.New()
	tray.SetIcon(trayIcon)
	tray.SetTooltip("KSpeech 实时字幕")
	tray.OnClick(service.ShowConsole)
	menu := app.Menu.New()
	menu.Add("显示主窗口").OnClick(func(*application.Context) { service.ShowConsole() })
	menu.Add("隐藏主窗口").OnClick(func(*application.Context) { service.HideConsole() })
	menu.Add("解除锁定").OnClick(func(*application.Context) { _ = service.SetLocked(false) })
	menu.Add("重置窗口位置").OnClick(func(*application.Context) { _ = service.ResetConsoleWindow() })
	menu.AddSeparator()
	menu.Add("设置").OnClick(func(*application.Context) { service.OpenSettings() })
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) { service.Quit() })
	tray.SetMenu(menu)

	service.attachRuntime(app, notificationService, consoleWindow, settingsWindow)
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func debugBrowserArgs() []string {
	port := strings.TrimSpace(os.Getenv("KSPEECH_WEBVIEW_DEBUG_PORT"))
	if port == "" {
		return nil
	}
	return []string{"--remote-debugging-port=" + port}
}
