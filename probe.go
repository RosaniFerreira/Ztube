package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Seg struct {
	A    float64 `json:"a"`
	B    float64 `json:"b"` // 0 = até o fim
	Name string  `json:"name"`
	S    bool    `json:"s"`
}

type Info struct {
	Token     int     `json:"token"`
	URL       string  `json:"url"`
	Kind      string  `json:"kind"` // video, live, canal, playlist, mix
	Title     string  `json:"title"`
	Uploader  string  `json:"uploader"`
	Thumb     string  `json:"thumb"`
	Duration  float64 `json:"duration"`
	Count     int     `json:"count"`
	IsLive    bool    `json:"isLive"`
	LiveSince int64   `json:"liveSince"`
	Chapters  []Seg   `json:"chapters"`
	DescSegs  []Seg   `json:"descSegs"`
	Err       string  `json:"err"`
}

var (
	reChannel  = regexp.MustCompile(`(?i)youtube\.com/(@[^/?#]+|channel/[^/?#]+|c/[^/?#]+|user/[^/?#]+)(/[a-z]+)?/?(\?.*)?$`)
	rePlaylist = regexp.MustCompile(`(?i)youtube\.com/playlist\?`)
	reHasList  = regexp.MustCompile(`[?&]list=`)
	reLive     = regexp.MustCompile(`(?i)youtube\.com/live/`)
)

// Classifica o link na hora, antes mesmo de consultar o YouTube.
func classify(url string) (kind, norm string) {
	u := strings.TrimSpace(url)
	switch {
	case reChannel.MatchString(u) && !strings.Contains(u, "/watch"):
		m := reChannel.FindStringSubmatch(u)
		if m[2] == "" { // sem aba: usa a de vídeos
			base := strings.TrimRight(strings.SplitN(u, "?", 2)[0], "/")
			return "canal", base + "/videos"
		}
		return "canal", u
	case rePlaylist.MatchString(u):
		return "playlist", u
	case reLive.MatchString(u):
		return "live", u
	case reHasList.MatchString(u):
		return "mix", u
	}
	return "video", u
}

func ytdlpEnv() []string {
	env := os.Environ()
	path := toolsDir
	for i, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), "PATH=") {
			env[i] = "PATH=" + path + ";" + e[5:]
			path = ""
		}
	}
	if path != "" {
		env = append(env, "PATH="+path)
	}
	return append(env, "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
}

func ytdlpJSON(timeout time.Duration, args ...string) (map[string]any, error) {
	yp := ytdlpPath()
	if yp == "" {
		return nil, errors.New("tools")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	base := []string{"-J", "--no-warnings", "--encoding", "utf-8"}
	if fp := ffmpegPath(); fp != "" {
		base = append(base, "--ffmpeg-location", fp)
	}
	cmd := exec.CommandContext(ctx, yp, append(base, args...)...)
	cmd.Env = ytdlpEnv()
	hideWindow(cmd)
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		msg := errb.String()
		if i := strings.LastIndex(msg, "ERROR:"); i >= 0 {
			msg = strings.TrimSpace(msg[i+6:])
		}
		if ctx.Err() != nil {
			msg = "timeout"
		}
		return nil, errors.New(firstLine(msg))
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, errors.New("resposta inválida")
	}
	return m, nil
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func num(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

func bestThumb(m map[string]any, preferAvatar bool) string {
	ths, _ := m["thumbnails"].([]any)
	if preferAvatar {
		for _, t := range ths {
			tm, _ := t.(map[string]any)
			if strings.Contains(str(tm, "id"), "avatar") {
				return str(tm, "url")
			}
		}
	}
	if s := str(m, "thumbnail"); s != "" {
		return s
	}
	// a de maior resolução que não seja gigante
	best, bw := "", 0.0
	for _, t := range ths {
		tm, _ := t.(map[string]any)
		w := num(tm, "width")
		if u := str(tm, "url"); u != "" && (best == "" || (w > bw && w <= 1280)) {
			best, bw = u, w
		}
	}
	return best
}

func friendlyProbeErr(e string) string {
	switch {
	case e == "tools":
		return "tools"
	case e == "timeout":
		return "O YouTube demorou demais para responder. Tente de novo."
	case strings.Contains(e, "Private video"):
		return "Esse vídeo é privado."
	case strings.Contains(e, "members"):
		return "Esse vídeo é exclusivo para membros do canal."
	case strings.Contains(e, "unavailable") || strings.Contains(e, "not available"):
		return "Esse vídeo não está disponível."
	case strings.Contains(e, "Unsupported URL") || strings.Contains(e, "is not a valid URL"):
		return "Esse link não foi reconhecido."
	case strings.Contains(e, "Sign in to confirm"):
		return "O YouTube pediu confirmação de login. Atualize o yt-dlp em Requisitos e tente de novo."
	}
	return "Não consegui ler esse link. Confira se ele está completo."
}

func probe(token int, url string) Info {
	kind, norm := classify(url)
	info := Info{Token: token, URL: norm, Kind: kind}
	var m map[string]any
	var err error
	switch kind {
	case "canal", "playlist":
		m, err = ytdlpJSON(3*time.Minute, "--flat-playlist", "--yes-playlist", norm)
	default:
		m, err = ytdlpJSON(90*time.Second, "--no-playlist", "--skip-download", norm)
	}
	if err != nil {
		info.Err = friendlyProbeErr(err.Error())
		return info
	}
	info.Title = str(m, "title")
	info.Uploader = str(m, "uploader")
	if info.Uploader == "" {
		info.Uploader = str(m, "channel")
	}
	info.Duration = num(m, "duration")
	if kind == "canal" || kind == "playlist" {
		entries, _ := m["entries"].([]any)
		info.Count = int(num(m, "playlist_count"))
		if info.Count == 0 {
			info.Count = len(entries)
		}
		info.Thumb = bestThumb(m, kind == "canal")
		if info.Thumb == "" && len(entries) > 0 {
			if e, ok := entries[0].(map[string]any); ok {
				info.Thumb = bestThumb(e, false)
			}
		}
		if kind == "canal" {
			t := info.Uploader
			if t == "" {
				t = strings.TrimSuffix(info.Title, " - Videos")
			}
			info.Title = t
			if h := str(m, "uploader_id"); h != "" {
				info.Uploader = h
			}
		}
		return info
	}
	info.Thumb = bestThumb(m, false)
	if ls := str(m, "live_status"); ls == "is_live" || m["is_live"] == true {
		info.IsLive = true
		info.Kind = "live"
		info.LiveSince = int64(num(m, "release_timestamp"))
		if info.LiveSince == 0 {
			info.LiveSince = int64(num(m, "timestamp"))
		}
	}
	if chs, ok := m["chapters"].([]any); ok {
		for _, c := range chs {
			cm, _ := c.(map[string]any)
			info.Chapters = append(info.Chapters, Seg{A: num(cm, "start_time"), B: num(cm, "end_time"), Name: str(cm, "title"), S: true})
		}
	}
	info.DescSegs = parseStamps(str(m, "description"), info.Duration)
	return info
}

// ---------- tempos escritos em texto ----------

var (
	reStampStart = regexp.MustCompile(`^\s*(?:[-–•*·▶►]\s*)?[\(\[]?((?:\d{1,2}:)?\d{1,2}:\d{2})[\)\]]?\s*(?:[-–—:|.]\s*)?(.*)$`)
	reStampEnd   = regexp.MustCompile(`^\s*(?:[-–•*·▶►]\s*)?(.+?)\s*[-–—:|]?\s*[\(\[]?((?:\d{1,2}:)?\d{1,2}:\d{2})[\)\]]?\s*$`)
)

func parseClock(s string) float64 {
	parts := strings.Split(s, ":")
	t := 0.0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return -1
		}
		t = t*60 + float64(n)
	}
	return t
}

// Lê listas como "0:12 Moeda" ou "Moeda - 0:12" e transforma em fatias.
func parseStamps(text string, dur float64) []Seg {
	type st struct {
		t    float64
		name string
	}
	var list []st
	seen := map[float64]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || len(line) > 200 {
			continue
		}
		var t float64 = -1
		var name string
		if m := reStampStart.FindStringSubmatch(line); m != nil {
			t, name = parseClock(m[1]), m[2]
		} else if m := reStampEnd.FindStringSubmatch(line); m != nil {
			t, name = parseClock(m[2]), m[1]
		}
		if t < 0 || seen[t] || (dur > 0 && t >= dur) {
			continue
		}
		seen[t] = true
		list = append(list, st{t, strings.Trim(strings.TrimSpace(name), "-–—:| ")})
	}
	if len(list) < 2 {
		return nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].t < list[j].t })
	var segs []Seg
	for i, s := range list {
		b := dur
		if i+1 < len(list) {
			b = list[i+1].t
		}
		name := s.name
		if name == "" {
			name = "Parte " + strconv.Itoa(i+1)
		}
		segs = append(segs, Seg{A: s.t, B: b, Name: name, S: true})
	}
	return segs
}

// Procura tempos no comentário fixado (ou num comentário do próprio canal).
func pinnedSegs(url string, dur float64) ([]Seg, error) {
	m, err := ytdlpJSON(2*time.Minute, "--no-playlist", "--skip-download", "--write-comments",
		"--extractor-args", "youtube:max_comments=40,all,0,0;comment_sort=top", url)
	if err != nil {
		return nil, err
	}
	cs, _ := m["comments"].([]any)
	var fallback []Seg
	for _, c := range cs {
		cm, _ := c.(map[string]any)
		segs := parseStamps(str(cm, "text"), dur)
		if len(segs) < 2 {
			continue
		}
		if cm["is_pinned"] == true {
			return segs, nil
		}
		if cm["author_is_uploader"] == true && fallback == nil {
			fallback = segs
		}
	}
	return fallback, nil
}
