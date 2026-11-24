package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type JobReq struct {
	URL      string  `json:"url"`
	Kind     string  `json:"kind"`
	Title    string  `json:"title"`
	Uploader string  `json:"uploader"`
	Thumb    string  `json:"thumb"`
	Duration float64 `json:"duration"`
	Mode     string  `json:"mode"` // v ou a
	Quality  int     `json:"quality"`
	Audio    int     `json:"audio"`
	Folder   string  `json:"folder"`
	LiveFrom string  `json:"liveFrom"` // "" = do início
	MixAll   bool    `json:"mixAll"`
	Slice    bool    `json:"slice"`
	SliceSrc string  `json:"sliceSrc"` // segs ou silence
	Segments []Seg   `json:"segments"`
	Silence  int     `json:"silence"` // 1 a 5
}

type Job struct {
	ID    string  `json:"id"`
	Req   JobReq  `json:"req"`
	State string  `json:"state"` // queued, running, paused, done, error, canceled
	Pct   float64 `json:"pct"`
	Indet bool    `json:"indet"`
	Stage string  `json:"stage"`
	Speed string  `json:"speed"`
	ETA   string  `json:"eta"`
	Size  string  `json:"size"`
	Item  string  `json:"item"`
	Err   string  `json:"err"`
	Path  string  `json:"path"`
	Parts int     `json:"parts"`

	log      []string
	cmd      *exec.Cmd
	stop     string
	lastEmit time.Time
}

var (
	jobsMu sync.Mutex
	jobs   []*Job
	wake   = make(chan struct{}, 1)
)

var videoSorts = []string{
	"vcodec:h264,res,acodec:aac",     // compatível com editores
	"res,fps,acodec:aac",             // máxima qualidade
	"res:720,vcodec:h264,acodec:aac", // 720p
	"res:480,vcodec:h264,acodec:aac", // 480p
}
var audioFmts = []string{"wav", "mp3", "m4a", "flac", "opus"}

func notify() {
	select {
	case wake <- struct{}{}:
	default:
	}
}

func snapshot(j *Job) Job {
	c := *j
	c.log, c.cmd = nil, nil
	return c
}

func listJobs() []Job {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	out := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, snapshot(j))
	}
	return out
}

func emitJob(j *Job, force bool) {
	jobsMu.Lock()
	if !force && time.Since(j.lastEmit) < 250*time.Millisecond {
		jobsMu.Unlock()
		return
	}
	j.lastEmit = time.Now()
	s := snapshot(j)
	jobsMu.Unlock()
	emit("job", s)
}

func findJob(id string) *Job {
	for _, j := range jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func startJob(r JobReq) string {
	j := &Job{ID: newID(), Req: r, State: "queued", Indet: true}
	jobsMu.Lock()
	jobs = append(jobs, j)
	jobsMu.Unlock()
	emitJob(j, true)
	notify()
	return j.ID
}

func stopJob(id, reason string) {
	jobsMu.Lock()
	j := findJob(id)
	if j == nil {
		jobsMu.Unlock()
		return
	}
	if j.State == "running" && j.cmd != nil && j.cmd.Process != nil {
		j.stop = reason
		pid := j.cmd.Process.Pid
		jobsMu.Unlock()
		killTree(pid)
		return
	}
	if j.State == "queued" || j.State == "paused" || j.State == "error" {
		j.State = reason
	}
	jobsMu.Unlock()
	emitJob(j, true)
}

func resumeJob(id string) {
	jobsMu.Lock()
	j := findJob(id)
	if j != nil && (j.State == "paused" || j.State == "error" || j.State == "canceled") {
		j.State, j.Err, j.Pct, j.Indet, j.stop = "queued", "", 0, true, ""
	}
	jobsMu.Unlock()
	if j != nil {
		emitJob(j, true)
		notify()
	}
}

func removeJob(id string) {
	jobsMu.Lock()
	for i, j := range jobs {
		if j.ID == id && j.State != "running" {
			jobs = append(jobs[:i], jobs[i+1:]...)
			break
		}
	}
	jobsMu.Unlock()
	emit("jobs", listJobs())
}

func jobLog(id string) string {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	if j := findJob(id); j != nil {
		return strings.Join(j.log, "\n")
	}
	return ""
}

func stopAll() {
	jobsMu.Lock()
	var pids []int
	for _, j := range jobs {
		if j.State == "running" && j.cmd != nil && j.cmd.Process != nil {
			pids = append(pids, j.cmd.Process.Pid)
		}
	}
	jobsMu.Unlock()
	for _, p := range pids {
		killTree(p)
	}
}

// Um download por vez; os outros esperam na fila.
func runner() {
	for {
		jobsMu.Lock()
		var next *Job
		for _, j := range jobs {
			if j.State == "queued" {
				next = j
				break
			}
		}
		if next != nil {
			next.State = "running"
			next.Stage = "Começando…"
		}
		jobsMu.Unlock()
		if next == nil {
			<-wake
			continue
		}
		emitJob(next, true)
		runJob(next)
	}
}

func (j *Job) addLog(l string) {
	jobsMu.Lock()
	j.log = append(j.log, l)
	if len(j.log) > 3000 {
		j.log = j.log[len(j.log)-2000:]
	}
	jobsMu.Unlock()
}

func (j *Job) set(f func()) {
	jobsMu.Lock()
	f()
	jobsMu.Unlock()
}

func buildArgs(r JobReq, listFile string) []string {
	a := []string{"--newline", "--encoding", "utf-8", "--no-colors",
		"--progress-template", "download:ZTPROG|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|%(progress.total_bytes_estimate)s|%(progress.speed)s|%(progress.eta)s|%(progress.fragment_index)s|%(progress.fragment_count)s",
		"--print-to-file", "after_move:%(filepath)s", listFile,
	}
	if fp := ffmpegPath(); fp != "" {
		a = append(a, "--ffmpeg-location", fp)
	}
	if r.Mode == "a" {
		f := audioFmts[clampIdx(r.Audio, len(audioFmts))]
		a = append(a, "-f", "ba/b", "-x", "--audio-format", f, "--audio-quality", "0")
	} else {
		a = append(a, "-f", "bv*+ba/b", "-S", videoSorts[clampIdx(r.Quality, len(videoSorts))], "--merge-output-format", "mp4")
	}
	out := "%(title)s.%(ext)s"
	switch r.Kind {
	case "live":
		a = append(a, "--live-from-start", "--no-playlist")
		if r.LiveFrom != "" {
			a = append(a, "--download-sections", "*"+r.LiveFrom+"-inf")
		}
	case "canal", "playlist":
		a = append(a, "--yes-playlist")
		out = "%(playlist_title,uploader)s/%(title)s.%(ext)s"
	case "mix":
		if r.MixAll {
			a = append(a, "--yes-playlist")
			out = "%(playlist_title)s/%(title)s.%(ext)s"
		} else {
			a = append(a, "--no-playlist")
		}
	default:
		a = append(a, "--no-playlist")
	}
	return append(a, "-P", r.Folder, "-o", out, "--", r.URL)
}

func clampIdx(i, n int) int {
	if i < 0 || i >= n {
		return 0
	}
	return i
}

// Separa linhas tanto por \n quanto por \r.
func scanLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

var reItem = regexp.MustCompile(`Downloading item (\d+) of (\d+)`)

func parseF(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

func (j *Job) handleLine(l string) {
	if strings.HasPrefix(l, "ZTPROG|") {
		p := strings.Split(l, "|")
		for len(p) < 8 {
			p = append(p, "")
		}
		dl, tot, est, spd, eta, fi, fc := parseF(p[1]), parseF(p[2]), parseF(p[3]), parseF(p[4]), parseF(p[5]), parseF(p[6]), parseF(p[7])
		j.set(func() {
			j.Indet = false
			switch {
			case tot > 0:
				j.Pct = dl / tot * 100
				j.Size = humanBytes(dl) + " de " + humanBytes(tot)
			case est > 0:
				j.Pct = math.Min(dl/est*100, 99)
				j.Size = humanBytes(dl) + " de ~" + humanBytes(est)
			case fc > 0:
				j.Pct = fi / fc * 100
				j.Size = humanBytes(dl)
			default:
				j.Indet = true
				j.Size = humanBytes(dl) + " gravados"
			}
			j.Speed, j.ETA = "", ""
			if spd > 0 {
				j.Speed = humanBytes(spd) + "/s"
			}
			if eta > 0 {
				j.ETA = clock(eta)
			}
			if j.Stage == "" || !strings.HasPrefix(j.Stage, "Baixando") {
				j.Stage = "Baixando"
			}
		})
		emitJob(j, false)
		return
	}
	j.addLog(l)
	switch {
	case reItem.MatchString(l):
		m := reItem.FindStringSubmatch(l)
		j.set(func() { j.Item = m[1] + " de " + m[2] })
	case strings.HasPrefix(l, "[Merger]"):
		j.set(func() { j.Stage, j.Indet, j.Speed, j.ETA = "Juntando vídeo e áudio", true, "", "" })
	case strings.HasPrefix(l, "[ExtractAudio]"):
		j.set(func() { j.Stage, j.Indet, j.Speed, j.ETA = "Convertendo o áudio", true, "", "" })
	case strings.Contains(l, "has already been downloaded"):
		j.set(func() { j.Stage = "Já estava na pasta" })
	case strings.HasPrefix(l, "ERROR:"):
		msg := strings.TrimSpace(strings.TrimPrefix(l, "ERROR:"))
		j.set(func() { j.Err = msg })
	default:
		return
	}
	emitJob(j, true)
}

func friendlyErr(e string) string {
	switch {
	case e == "":
		return "O download parou com erro. Veja o que aconteceu nos detalhes."
	case strings.Contains(e, "Private video"):
		return "Esse vídeo é privado."
	case strings.Contains(e, "members") || strings.Contains(e, "Join this channel"):
		return "Esse vídeo é exclusivo para membros do canal."
	case strings.Contains(e, "Video unavailable") || strings.Contains(e, "not available"):
		return "Esse vídeo não está disponível."
	case strings.Contains(e, "Sign in to confirm"):
		return "O YouTube pediu confirmação de login. Atualize o yt-dlp em Requisitos e tente de novo."
	case strings.Contains(e, "ffmpeg") || strings.Contains(e, "ffprobe"):
		return "O FFmpeg não foi encontrado. Instale em Requisitos."
	case strings.Contains(e, "HTTP Error 403") || strings.Contains(e, "nsig") || strings.Contains(e, "Requested format"):
		return "O YouTube mudou alguma coisa. Atualize o yt-dlp em Requisitos e tente de novo."
	case strings.Contains(e, "No space left"):
		return "Não há espaço livre suficiente no disco."
	}
	if len([]rune(e)) > 160 {
		e = string([]rune(e)[:160]) + "…"
	}
	return e
}

func runJob(j *Job) {
	r := j.Req
	fail := func(msg string) {
		j.set(func() { j.State, j.Err, j.Indet = "error", msg, false })
		emitJob(j, true)
	}
	yp := ytdlpPath()
	if yp == "" || ffmpegPath() == "" {
		fail("Faltam ferramentas. Instale o yt-dlp e o FFmpeg em Requisitos.")
		return
	}
	if err := os.MkdirAll(r.Folder, 0o755); err != nil {
		fail("Não consegui acessar a pasta escolhida.")
		return
	}
	listFile := filepath.Join(os.TempDir(), "ztube-"+j.ID+".txt")
	os.Remove(listFile)
	defer os.Remove(listFile)

	args := buildArgs(r, listFile)
	cmd := exec.Command(yp, args...)
	cmd.Env = ytdlpEnv()
	hideWindow(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	j.addLog("> yt-dlp " + strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		fail("Não consegui abrir o yt-dlp.")
		return
	}
	j.set(func() { j.cmd = cmd; j.Err = "" })
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sc.Split(scanLines)
		for sc.Scan() {
			if l := strings.TrimSpace(sc.Text()); l != "" {
				j.handleLine(l)
			}
		}
		close(done)
	}()
	werr := cmd.Wait()
	pw.Close()
	<-done

	var stop string
	j.set(func() { stop = j.stop; j.cmd = nil; j.stop = "" })
	if stop != "" {
		j.set(func() { j.State, j.Stage, j.Speed, j.ETA, j.Indet = stop, "", "", "", false })
		emitJob(j, true)
		return
	}
	var files []string
	if b, err := os.ReadFile(listFile); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				files = append(files, l)
			}
		}
	}
	if werr != nil && len(files) == 0 {
		var e string
		j.set(func() { e = j.Err })
		fail(friendlyErr(e))
		return
	}

	path := r.Folder
	parts := 0
	kind := "video"
	if r.Mode == "a" {
		kind = "audio"
	}
	if len(files) == 1 {
		path = files[0]
	} else if len(files) > 1 {
		path = filepath.Dir(files[0])
		kind = "pasta"
	}
	if r.Slice && len(files) == 1 {
		dir, n, err := sliceFile(j, files[0])
		if err != nil {
			fail(err.Error())
			return
		}
		path, parts, kind = dir, n, "pasta"
	}

	desc := describe(r, parts, len(files))
	j.set(func() {
		j.State, j.Pct, j.Indet, j.Stage, j.Speed, j.ETA, j.Path, j.Parts = "done", 100, false, "", "", "", path, parts
	})
	emitJob(j, true)
	title := r.Title
	if title == "" {
		title = r.URL
	}
	addHistory(HistItem{ID: j.ID, Title: title, Desc: desc, When: time.Now().UnixMilli(), Path: path, Kind: kind, Thumb: r.Thumb})
	// tira da lista "em andamento" depois de um tempinho
	go func() {
		time.Sleep(4 * time.Second)
		removeJob(j.ID)
	}()
}

func describe(r JobReq, parts, files int) string {
	format := "Vídeo MP4"
	if r.Mode == "a" {
		format = "Áudio " + strings.ToUpper(audioFmts[clampIdx(r.Audio, len(audioFmts))])
	}
	switch {
	case parts > 0:
		return fmt.Sprintf("%d partes, %s", parts, strings.ToLower(format[:1])+format[1:])
	case files > 1:
		return fmt.Sprintf("%d arquivos, %s", files, strings.ToLower(format[:1])+format[1:])
	}
	return format
}

// ---------- fatiar ----------

func audioCodecArgs(ext string) []string {
	switch ext {
	case "mp3":
		return []string{"-c:a", "libmp3lame", "-q:a", "0"}
	case "wav":
		return []string{"-c:a", "pcm_s16le"}
	case "m4a":
		return []string{"-c:a", "aac", "-b:a", "256k"}
	case "flac":
		return []string{"-c:a", "flac"}
	case "opus":
		return []string{"-c:a", "libopus", "-b:a", "160k"}
	}
	return []string{"-c", "copy"}
}

var (
	reSilStart = regexp.MustCompile(`silence_start: (-?[\d.]+)`)
	reSilEnd   = regexp.MustCompile(`silence_end: (-?[\d.]+)`)
	reDur      = regexp.MustCompile(`Duration: (\d+):(\d+):([\d.]+)`)
)

// Acha os trechos com som, separados por silêncio.
func silenceSegs(ff, file string, level int) ([]Seg, error) {
	if level < 1 || level > 5 {
		level = 3
	}
	noise := []int{-50, -45, -40, -35, -30}[level-1]
	minDur := []float64{0.7, 0.55, 0.4, 0.3, 0.2}[level-1]
	cmd := exec.Command(ff, "-hide_banner", "-nostats", "-i", file, "-af",
		fmt.Sprintf("silencedetect=noise=%ddB:d=%.2f", noise, minDur), "-f", "null", "-")
	hideWindow(cmd)
	out, _ := cmd.CombinedOutput()
	s := string(out)
	total := 0.0
	if m := reDur.FindStringSubmatch(s); m != nil {
		total = parseF(m[1])*3600 + parseF(m[2])*60 + parseF(m[3])
	}
	starts := reSilStart.FindAllStringSubmatch(s, -1)
	ends := reSilEnd.FindAllStringSubmatch(s, -1)
	var segs []Seg
	cur := 0.0
	for i, st := range starts {
		a := parseF(st[1])
		if a-cur > 0.3 {
			segs = append(segs, Seg{A: cur, B: a, S: true})
		}
		if i < len(ends) {
			cur = parseF(ends[i][1])
		} else {
			cur = total
		}
	}
	if total-cur > 0.3 {
		segs = append(segs, Seg{A: cur, B: total, S: true})
	}
	for i := range segs {
		segs[i].Name = fmt.Sprintf("Parte %d", i+1)
	}
	if len(segs) < 2 {
		return nil, fmt.Errorf("Não encontrei pausas suficientes para fatiar. Tente uma sensibilidade maior.")
	}
	return segs, nil
}

func sliceFile(j *Job, file string) (string, int, error) {
	r := j.Req
	ff := ffmpegPath()
	j.set(func() { j.Stage, j.Indet, j.Pct, j.Speed, j.ETA, j.Size = "Procurando as pausas", true, 0, "", "", "" })
	emitJob(j, true)
	var segs []Seg
	if r.SliceSrc == "silence" {
		s, err := silenceSegs(ff, file, r.Silence)
		if err != nil {
			return "", 0, err
		}
		segs = s
	} else {
		for _, s := range r.Segments {
			if s.S {
				segs = append(segs, s)
			}
		}
	}
	if len(segs) == 0 {
		return "", 0, fmt.Errorf("Nenhuma fatia selecionada.")
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(file)), ".")
	title := r.Title
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}
	outDir := filepath.Join(filepath.Dir(file), safeName(title))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", 0, fmt.Errorf("Não consegui criar a pasta das fatias.")
	}
	codec := []string{"-c", "copy", "-avoid_negative_ts", "make_zero"}
	if r.Mode == "a" {
		codec = audioCodecArgs(ext)
	}
	width := 2
	if len(segs) >= 100 {
		width = 3
	}
	for i, s := range segs {
		j.set(func() {
			j.Stage = fmt.Sprintf("Fatiando: parte %d de %d", i+1, len(segs))
			j.Indet = false
			j.Pct = float64(i) / float64(len(segs)) * 100
		})
		emitJob(j, true)
		name := fmt.Sprintf("%0*d - %s.%s", width, i+1, safeName(s.Name), ext)
		args := []string{"-hide_banner", "-nostats", "-loglevel", "error", "-y", "-ss", strconv.FormatFloat(s.A, 'f', 3, 64), "-i", file}
		if s.B > s.A {
			args = append(args, "-t", strconv.FormatFloat(s.B-s.A, 'f', 3, 64))
		}
		args = append(args, "-map", "0:a?", "-map", "0:v?")
		args = append(args, codec...)
		args = append(args, filepath.Join(outDir, name))
		cmd := exec.Command(ff, args...)
		hideWindow(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			j.addLog(string(out))
			return "", 0, fmt.Errorf("O FFmpeg não conseguiu cortar a parte %d.", i+1)
		}
	}
	_ = os.Remove(file)
	return outDir, len(segs), nil
}
