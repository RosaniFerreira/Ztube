// ZTube — interface para baixar vídeos e áudios do YouTube com o yt-dlp.
// Criado por Rosani Ferreira.
package main

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/harry1453/go-common-file-dialog/cfd"
	webview2 "github.com/jchv/go-webview2"
)

const appVersion = "1.0.0"

//go:embed ui/index.html
var indexHTML string

var (
	w      webview2.WebView
	hwnd   uintptr
	ready  bool
	readyM sync.Mutex
)

// Envia um evento para a interface.
func emit(name string, data any) {
	readyM.Lock()
	ok := ready
	readyM.Unlock()
	if !ok || w == nil {
		return
	}
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	n, _ := json.Marshal(name)
	js := "window.ztEvent && window.ztEvent(" + string(n) + "," + string(b) + ")"
	w.Dispatch(func() { w.Eval(js) })
}

func main() {
	runtime.LockOSThread()
	setDPIAware()
	initPaths()
	loadSettings()
	loadHistory()

	scale := systemScale()
	cw, ch := int(1200*scale+0.5), int(700*scale+0.5)
	w = webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(appDir, "webview"),
		WindowOptions: webview2.WindowOptions{
			Title:  "ZTube",
			Width:  uint(cw),
			Height: uint(ch),
			IconId: 2,
			Center: true,
		},
	})
	if w == nil {
		r := messageBox("ZTube",
			"O ZTube precisa do Microsoft Edge WebView2, que não foi encontrado neste computador.\n\nQuer abrir a página da Microsoft para instalar?", 0x04|0x30)
		if r == 6 {
			shellOpen("https://developer.microsoft.com/microsoft-edge/webview2/")
		}
		return
	}
	defer w.Destroy()
	hwnd = uintptr(w.Window())
	w.SetSize(cw, ch, webview2.HintFixed)
	centerWindow(hwnd)
	setDarkTitle(hwnd, settings.Tema == "dark")

	bind()
	w.SetHtml(indexHTML)

	go runner()
	w.Run()
	stopAll()
}

func bind() {
	w.Bind("zt_ready", func() map[string]any {
		readyM.Lock()
		ready = true
		readyM.Unlock()
		settingsMu.Lock()
		s := settings
		settingsMu.Unlock()
		go refreshTools(true)
		return map[string]any{
			"settings": s,
			"history":  getHistory(),
			"jobs":     listJobs(),
			"version":  appVersion,
			"toolsDir": toolsDir,
			"hasYt":    ytdlpPath() != "",
			"hasFF":    ffmpegPath() != "",
		}
	})
	w.Bind("zt_saveSettings", func(s Settings) { go saveSettings(s) })
	w.Bind("zt_clipboard", func() string { return strings.TrimSpace(clipboardText(hwnd)) })
	w.Bind("zt_darkTitle", func(dark bool) { setDarkTitle(hwnd, dark) })
	w.Bind("zt_pickFolder", func(cur string) string {
		d, err := cfd.NewSelectFolderDialog(cfd.DialogConfig{
			Title:              "Escolha onde salvar",
			Role:               "ZTubePasta",
			Folder:             cur,
			ParentWindowHandle: hwnd,
		})
		if err != nil {
			return ""
		}
		defer d.Release()
		res, err := d.ShowAndGetResult()
		if err != nil {
			return ""
		}
		return res
	})
	w.Bind("zt_open", func(p string) {
		if p != "" {
			if _, err := os.Stat(p); err == nil {
				shellOpen(p)
				return
			}
			emit("toast", "Esse arquivo não está mais nesse lugar.")
		}
	})
	w.Bind("zt_reveal", func(p string) {
		if _, err := os.Stat(p); err == nil {
			revealInExplorer(p)
			return
		}
		if _, err := os.Stat(filepath.Dir(p)); err == nil {
			shellOpen(filepath.Dir(p))
			return
		}
		emit("toast", "Essa pasta não existe mais.")
	})
	w.Bind("zt_openFolder", func(p string) {
		if p == "" {
			return
		}
		_ = os.MkdirAll(p, 0o755)
		shellOpen(p)
	})
	w.Bind("zt_url", func(u string) {
		if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
			shellOpen(u)
		}
	})
	w.Bind("zt_classify", func(u string) map[string]string {
		k, n := classify(u)
		return map[string]string{"kind": k, "url": n}
	})
	w.Bind("zt_probe", func(token int, u string) {
		go func() { emit("probe", probe(token, u)) }()
	})
	w.Bind("zt_pinned", func(token int, u string, dur float64) {
		go func() {
			segs, err := pinnedSegs(u, dur)
			res := map[string]any{"token": token, "segs": segs}
			if err != nil {
				res["err"] = friendlyProbeErr(err.Error())
			}
			emit("pinned", res)
		}()
	})
	w.Bind("zt_start", func(r JobReq) string { return startJob(r) })
	w.Bind("zt_pause", func(id string) { go stopJob(id, "paused") })
	w.Bind("zt_cancel", func(id string) { go stopJob(id, "canceled") })
	w.Bind("zt_resume", func(id string) { resumeJob(id) })
	w.Bind("zt_remove", func(id string) { removeJob(id) })
	w.Bind("zt_log", func(id string) string { return jobLog(id) })
	w.Bind("zt_jobs", func() []Job { return listJobs() })
	w.Bind("zt_removeHistory", func(id string) { go removeHistory(id) })
	w.Bind("zt_clearHistory", func() { go clearHistory() })
	w.Bind("zt_checkTools", func() { go refreshTools(true) })
	w.Bind("zt_install", func(id string) { go installTool(id) })
	w.Bind("zt_openTools", func() { shellOpen(toolsDir) })
}
