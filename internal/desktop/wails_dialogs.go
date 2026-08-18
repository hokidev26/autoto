//go:build desktop

package desktop

import (
	"context"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"autoto/internal/server"
)

// WailsDialogHost shows OS-native dialogs through Wails.
// Methods are safe to call from HTTP handlers because Wails dialog methods
// marshal their own work onto the UI thread.
type WailsDialogHost struct {
	app    *application.App
	window *application.WebviewWindow
	mu     sync.RWMutex
}

// NewWailsDialogHost creates a host bound to a Wails application. Window may be
// set later with AttachWindow so dialogs can be sheet-attached on macOS.
func NewWailsDialogHost(app *application.App) *WailsDialogHost {
	return &WailsDialogHost{app: app}
}

// AttachWindow associates dialogs with the main window when available.
func (h *WailsDialogHost) AttachWindow(window *application.WebviewWindow) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.window = window
	h.mu.Unlock()
}

// Confirm implements server.ShellDialogHost.
//
// Wails message dialogs complete via button callbacks (async on macOS sheet /
// Linux). We must never read the result immediately after Show() returns.
// Windows maps Question dialogs to Yes/No labels, so use those labels.
func (h *WailsDialogHost) Confirm(ctx context.Context, message, title string) (bool, error) {
	if h == nil || h.app == nil {
		return false, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if title == "" {
		title = "Autoto"
	}
	type result struct {
		accepted bool
		err      error
	}
	done := make(chan result, 1)
	var once sync.Once
	finish := func(accepted bool, err error) {
		once.Do(func() {
			done <- result{accepted: accepted, err: err}
		})
	}
	dialog := h.app.Dialog.Question().
		SetTitle(title).
		SetMessage(message)
	if window := h.mainWindow(); window != nil {
		dialog.AttachToWindow(window)
	}
	// Windows MessageBox Question returns "Yes"/"No". Custom labels only
	// work on macOS/Linux, so use the portable pair.
	yes := dialog.AddButton("Yes")
	yes.OnClick(func() { finish(true, nil) })
	dialog.SetDefaultButton(yes)
	no := dialog.AddButton("No")
	no.OnClick(func() { finish(false, nil) })
	dialog.SetCancelButton(no)
	// Show marshals to the UI thread itself. Wrapping it in application.InvokeSync
	// deadlocks because Wails would synchronously invoke the UI thread twice.
	dialog.Show()
	// Show may return before callbacks on sheet/async platforms; wait below.
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case res := <-done:
		return res.accepted, res.err
	}
}

// Alert implements server.ShellDialogHost.
func (h *WailsDialogHost) Alert(ctx context.Context, message, title string) error {
	if h == nil || h.app == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if title == "" {
		title = "Autoto"
	}
	done := make(chan error, 1)
	var once sync.Once
	finish := func(err error) {
		once.Do(func() { done <- err })
	}
	dialog := h.app.Dialog.Info().
		SetTitle(title).
		SetMessage(message)
	if window := h.mainWindow(); window != nil {
		dialog.AttachToWindow(window)
	}
	// Windows Info dialog maps to "Ok".
	ok := dialog.AddButton("Ok")
	ok.OnClick(func() { finish(nil) })
	dialog.SetDefaultButton(ok)
	dialog.Show()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// PickDirectory implements server.ShellDialogHost.
func (h *WailsDialogHost) PickDirectory(ctx context.Context, title, defaultPath string) (string, bool, error) {
	if h == nil || h.app == nil {
		return "", true, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return "", true, err
	}
	if title == "" {
		title = "Select folder"
	}
	type result struct {
		path     string
		canceled bool
		err      error
	}
	done := make(chan result, 1)
	dialog := h.app.Dialog.OpenFile().
		SetTitle(title).
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true)
	if window := h.mainWindow(); window != nil {
		dialog.AttachToWindow(window)
	}
	if dir := strings.TrimSpace(defaultPath); dir != "" {
		dialog.SetDirectory(dir)
	}
	// PromptForSingleSelection marshals to the UI thread internally. An outer
	// application.InvokeSync causes a nested synchronous invoke and freezes the
	// WebView before the native picker can appear.
	path, err := dialog.PromptForSingleSelection()
	if err != nil {
		msg := strings.ToLower(err.Error())
		if path == "" || strings.Contains(msg, "cancel") || strings.Contains(msg, "abort") {
			done <- result{canceled: true}
		} else {
			done <- result{err: err}
		}
	} else {
		path = strings.TrimSpace(path)
		if path == "" {
			done <- result{canceled: true}
		} else {
			done <- result{path: path}
		}
	}
	select {
	case <-ctx.Done():
		return "", true, ctx.Err()
	case res := <-done:
		return res.path, res.canceled, res.err
	}
}

// PickFile implements server.ShellDialogHost.
func (h *WailsDialogHost) PickFile(ctx context.Context, title, defaultPath string, filters []server.ShellFileFilter) (string, bool, error) {
	if h == nil || h.app == nil {
		return "", true, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return "", true, err
	}
	if title == "" {
		title = "Select file"
	}
	type result struct {
		path     string
		canceled bool
		err      error
	}
	done := make(chan result, 1)
	dialog := h.app.Dialog.OpenFile().
		SetTitle(title).
		CanChooseFiles(true).
		CanChooseDirectories(false)
	if window := h.mainWindow(); window != nil {
		dialog.AttachToWindow(window)
	}
	if dir := strings.TrimSpace(defaultPath); dir != "" {
		dialog.SetDirectory(dir)
	}
	for _, filter := range filters {
		name := strings.TrimSpace(filter.Name)
		pattern := strings.TrimSpace(filter.Pattern)
		if name == "" || pattern == "" {
			continue
		}
		dialog.AddFilter(name, pattern)
	}
	path, err := dialog.PromptForSingleSelection()
	if err != nil {
		msg := strings.ToLower(err.Error())
		if path == "" || strings.Contains(msg, "cancel") || strings.Contains(msg, "abort") {
			done <- result{canceled: true}
		} else {
			done <- result{err: err}
		}
	} else {
		path = strings.TrimSpace(path)
		if path == "" {
			done <- result{canceled: true}
		} else {
			done <- result{path: path}
		}
	}
	select {
	case <-ctx.Done():
		return "", true, ctx.Err()
	case res := <-done:
		return res.path, res.canceled, res.err
	}
}

func (h *WailsDialogHost) mainWindow() *application.WebviewWindow {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.window
}
