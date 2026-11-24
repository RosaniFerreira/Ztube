package main

import (
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	dwmapi  = windows.NewLazySystemDLL("dwmapi.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	pMessageBoxW                   = user32.NewProc("MessageBoxW")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pGetWindowRect                 = user32.NewProc("GetWindowRect")
	pSystemParametersInfoW         = user32.NewProc("SystemParametersInfoW")
	pDwmSetWindowAttribute         = dwmapi.NewProc("DwmSetWindowAttribute")
	pShellExecuteW                 = shell32.NewProc("ShellExecuteW")
	pOpenClipboard                 = user32.NewProc("OpenClipboard")
	pCloseClipboard                = user32.NewProc("CloseClipboard")
	pGetClipboardData              = user32.NewProc("GetClipboardData")
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	pGlobalLock                    = kernel32.NewProc("GlobalLock")
	pGlobalUnlock                  = kernel32.NewProc("GlobalUnlock")
)

// Texto da área de transferência.
func clipboardText(owner uintptr) string {
	if r, _, _ := pOpenClipboard.Call(owner); r == 0 {
		return ""
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGetClipboardData.Call(13) // CF_UNICODETEXT
	if h == 0 {
		return ""
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer pGlobalUnlock.Call(h)
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(p)))
}

type rect struct{ Left, Top, Right, Bottom int32 }

// Deixa o app nítido em telas com escala (125%, 150%...).
func setDPIAware() {
	if pSetProcessDpiAwarenessContext.Find() == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
		pSetProcessDpiAwarenessContext.Call(^uintptr(3))
	}
}

func systemScale() float64 {
	if pGetDpiForSystem.Find() == nil {
		if dpi, _, _ := pGetDpiForSystem.Call(); dpi >= 96 {
			return float64(dpi) / 96
		}
	}
	return 1
}

// Centraliza a janela na área útil da tela (sem a barra de tarefas).
func centerWindow(hwnd uintptr) {
	var wr, work rect
	pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	// SPI_GETWORKAREA = 0x30
	pSystemParametersInfoW.Call(0x30, 0, uintptr(unsafe.Pointer(&work)), 0)
	w, h := wr.Right-wr.Left, wr.Bottom-wr.Top
	x := work.Left + (work.Right-work.Left-w)/2
	y := work.Top + (work.Bottom-work.Top-h)/2
	if x < work.Left {
		x = work.Left
	}
	if y < work.Top {
		y = work.Top
	}
	// SWP_NOSIZE | SWP_NOZORDER
	pSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, 0x0001|0x0004)
}

// Barra de título escura ou clara, acompanhando o tema do app.
func setDarkTitle(hwnd uintptr, dark bool) {
	if pDwmSetWindowAttribute.Find() != nil {
		return
	}
	var v int32
	if dark {
		v = 1
	}
	pDwmSetWindowAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&v)), 4)
	pDwmSetWindowAttribute.Call(hwnd, 19, uintptr(unsafe.Pointer(&v)), 4)
	// força o redesenho da moldura
	pSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, 0x0001|0x0002|0x0004|0x0020)
}

func messageBox(title, text string, flags uintptr) int {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	r, _, _ := pMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags)
	return int(r)
}

// Abre um arquivo, pasta ou link com o programa padrão do Windows.
func shellOpen(target string) {
	verb, _ := windows.UTF16PtrFromString("open")
	p, _ := windows.UTF16PtrFromString(target)
	pShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(p)), 0, 0, 1)
}

// Abre o Explorador com o arquivo selecionado.
func revealInExplorer(path string) {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	_ = cmd.Start()
}

// Processos filhos sem abrir janelas de console.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// Encerra o processo e todos os filhos (o yt-dlp chama o FFmpeg).
func killTree(pid int) {
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	hideWindow(cmd)
	_ = cmd.Run()
}

func downloadsFolder() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0); err == nil && p != "" {
		return p
	}
	return ""
}

func webview2Version() string {
	const guid = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	keys := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + guid},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + guid},
		{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\` + guid},
	}
	for _, k := range keys {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := key.GetStringValue("pv")
		key.Close()
		if err == nil && v != "" && v != "0.0.0.0" {
			return v
		}
	}
	return ""
}
