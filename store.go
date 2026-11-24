package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	appDir   string // %LOCALAPPDATA%\ZTube
	toolsDir string // %LOCALAPPDATA%\ZTube\ferramentas
)

func initPaths() {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserConfigDir()
	}
	appDir = filepath.Join(base, "ZTube")
	toolsDir = filepath.Join(appDir, "ferramentas")
	_ = os.MkdirAll(toolsDir, 0o755)
}

// ---------- configurações ----------

type Settings struct {
	Pasta     string `json:"pasta"`
	Tema      string `json:"tema"` // "", "light" ou "dark"
	Modo      string `json:"modo"` // "v" ou "a"
	Qualidade int    `json:"qualidade"`
	Audio     int    `json:"audio"`
}

var (
	settings   Settings
	settingsMu sync.Mutex
)

func loadSettings() {
	settings = Settings{Pasta: downloadsFolder(), Modo: "v"}
	if b, err := os.ReadFile(filepath.Join(appDir, "config.json")); err == nil {
		_ = json.Unmarshal(b, &settings)
	}
	if settings.Pasta == "" {
		settings.Pasta = downloadsFolder()
	}
}

func saveSettings(s Settings) {
	settingsMu.Lock()
	settings = s
	b, _ := json.MarshalIndent(s, "", "  ")
	settingsMu.Unlock()
	_ = os.WriteFile(filepath.Join(appDir, "config.json"), b, 0o644)
}

// ---------- histórico (aba Concluídos) ----------

type HistItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Desc  string `json:"desc"`
	When  int64  `json:"when"` // unix ms
	Path  string `json:"path"`
	Kind  string `json:"kind"` // "audio", "video", "pasta"
	Thumb string `json:"thumb"`
}

var (
	history   []HistItem
	historyMu sync.Mutex
)

func loadHistory() {
	if b, err := os.ReadFile(filepath.Join(appDir, "historico.json")); err == nil {
		_ = json.Unmarshal(b, &history)
	}
}

func saveHistoryLocked() {
	b, _ := json.MarshalIndent(history, "", "  ")
	_ = os.WriteFile(filepath.Join(appDir, "historico.json"), b, 0o644)
}

func addHistory(h HistItem) {
	historyMu.Lock()
	history = append([]HistItem{h}, history...)
	if len(history) > 300 {
		history = history[:300]
	}
	saveHistoryLocked()
	historyMu.Unlock()
	emit("history", getHistory())
}

func getHistory() []HistItem {
	historyMu.Lock()
	defer historyMu.Unlock()
	out := make([]HistItem, len(history))
	copy(out, history)
	return out
}

func removeHistory(id string) {
	historyMu.Lock()
	for i, h := range history {
		if h.ID == id {
			history = append(history[:i], history[i+1:]...)
			break
		}
	}
	saveHistoryLocked()
	historyMu.Unlock()
	emit("history", getHistory())
}

func clearHistory() {
	historyMu.Lock()
	history = nil
	saveHistoryLocked()
	historyMu.Unlock()
	emit("history", getHistory())
}

// ---------- formatação ----------

func ptNum(v float64, dec int) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', dec, 64), ".", ",", 1)
}

func humanBytes(b float64) string {
	switch {
	case b >= 1<<30:
		return ptNum(b/(1<<30), 2) + " GB"
	case b >= 1<<20:
		return ptNum(b/(1<<20), 1) + " MB"
	case b >= 1<<10:
		return ptNum(b/(1<<10), 0) + " KB"
	}
	return ptNum(b, 0) + " B"
}

func clock(sec float64) string {
	if sec < 0 || math.IsNaN(sec) {
		sec = 0
	}
	s := int(sec + 0.5)
	h, m, x := s/3600, s%3600/60, s%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, x)
	}
	return fmt.Sprintf("%d:%02d", m, x)
}

var badName = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// Nome de arquivo válido no Windows.
func safeName(s string) string {
	s = badName.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, ". ")
	if r := []rune(s); len(r) > 120 {
		s = strings.TrimSpace(string(r[:120]))
	}
	if s == "" {
		s = "Sem nome"
	}
	switch strings.ToUpper(s) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "LPT1":
		s += "_"
	}
	return s
}

func newID() string { return strconv.FormatInt(time.Now().UnixNano(), 36) }
