package main

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Situação de cada ferramenta, do jeito que a página Requisitos mostra.
type Tool struct {
	ID      string  `json:"id"`
	Status  string  `json:"status"` // ok, old, miss, sys, busy, check
	Version string  `json:"version"`
	Latest  string  `json:"latest"`
	Size    string  `json:"size"`
	Source  string  `json:"source"` // ztube, sistema, windows
	Note    string  `json:"note"`
	Pct     float64 `json:"pct"`
	Text    string  `json:"text"`
	Err     string  `json:"err"`
}

var (
	toolsMu    sync.Mutex
	toolState  = map[string]*Tool{}
	latestRels = map[string]*ghRel{}
	httpClient = &http.Client{Timeout: 0}
)

// ---------- onde estão as ferramentas ----------

func localTool(name string) string {
	p := filepath.Join(toolsDir, name+".exe")
	if st, err := os.Stat(p); err == nil && st.Size() > 0 {
		return p
	}
	return ""
}

func systemTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func ytdlpPath() string {
	if p := localTool("yt-dlp"); p != "" {
		return p
	}
	return systemTool("yt-dlp")
}

func ffmpegPath() string {
	if p := localTool("ffmpeg"); p != "" {
		return p
	}
	return systemTool("ffmpeg")
}

func denoPath() string {
	if p := localTool("deno"); p != "" {
		return p
	}
	return systemTool("deno")
}

func runOut(timeout time.Duration, exe string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	hideWindow(cmd)
	b, _ := cmd.Output()
	return strings.TrimSpace(string(b))
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------- registro do que o ZTube instalou ----------

type manifest struct {
	FFmpegDate string `json:"ffmpeg_date"`
}

func readManifest() manifest {
	var m manifest
	if b, err := os.ReadFile(filepath.Join(toolsDir, "instalado.json")); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writeManifest(m manifest) {
	b, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(filepath.Join(toolsDir, "instalado.json"), b, 0o644)
}

// ---------- GitHub ----------

type ghAsset struct {
	Name    string `json:"name"`
	URL     string `json:"browser_download_url"`
	Size    int64  `json:"size"`
	Digest  string `json:"digest"`
	Updated string `json:"updated_at"`
}

type ghRel struct {
	Tag    string    `json:"tag_name"`
	Assets []ghAsset `json:"assets"`
}

func (r *ghRel) asset(name string) *ghAsset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

func ghGet(url string) (*http.Response, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "ZTube")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("o GitHub respondeu %d", resp.StatusCode)
	}
	return resp, nil
}

func ghRelease(repo, which string) (*ghRel, error) {
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	if which != "" {
		url = "https://api.github.com/repos/" + repo + "/releases/tags/" + which
	}
	resp, err := ghGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r ghRel
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

const (
	repoYtdlp  = "yt-dlp/yt-dlp"
	repoDeno   = "denoland/deno"
	repoFFmpeg = "yt-dlp/FFmpeg-Builds"
	assetYtdlp = "yt-dlp.exe"
	assetDeno  = "deno-x86_64-pc-windows-msvc.zip"
	assetFF    = "ffmpeg-master-latest-win64-gpl.zip"
)

func fetchLatest() {
	type res struct {
		key string
		rel *ghRel
	}
	ch := make(chan res, 3)
	go func() { r, _ := ghRelease(repoYtdlp, ""); ch <- res{"yt", r} }()
	go func() { r, _ := ghRelease(repoDeno, ""); ch <- res{"deno", r} }()
	go func() { r, _ := ghRelease(repoFFmpeg, "latest"); ch <- res{"ff", r} }()
	for i := 0; i < 3; i++ {
		r := <-ch
		if r.rel != nil {
			toolsMu.Lock()
			latestRels[r.key] = r.rel
			toolsMu.Unlock()
		}
	}
}

// ---------- verificação ----------

func versionYtdlp(p string) string { return firstLine(runOut(20*time.Second, p, "--version")) }

func versionDeno(p string) string {
	l := firstLine(runOut(20*time.Second, p, "--version")) // "deno 2.9.8 (stable, ...)"
	f := strings.Fields(l)
	if len(f) >= 2 {
		return f[1]
	}
	return ""
}

func versionFFmpeg(p string) string {
	l := firstLine(runOut(20*time.Second, p, "-version")) // "ffmpeg version N-1234-g... Copyright"
	f := strings.Fields(l)
	if len(f) >= 3 {
		return f[2]
	}
	return ""
}

func mb(b int64) string { return fmt.Sprintf("%d MB", (b+(1<<20)/2)>>20) }

// Monta o estado das ferramentas. remote=true consulta o GitHub.
func checkTools(remote bool) []Tool {
	if remote {
		fetchLatest()
	}
	toolsMu.Lock()
	ytRel, denoRel, ffRel := latestRels["yt"], latestRels["deno"], latestRels["ff"]
	busy := map[string]*Tool{}
	for k, v := range toolState {
		if v.Status == "busy" {
			busy[k] = v
		}
	}
	toolsMu.Unlock()

	var out []Tool

	// yt-dlp (+ Deno, instalado junto)
	if b, ok := busy["yt"]; ok {
		out = append(out, *b)
	} else {
		t := Tool{ID: "yt"}
		p := ytdlpPath()
		dp := denoPath()
		var size int64
		if p == "" {
			t.Status = "miss"
		} else {
			t.Version = versionYtdlp(p)
			t.Status = "ok"
			t.Source = "ztube"
			if localTool("yt-dlp") == "" {
				t.Source = "sistema"
			}
			if ytRel != nil {
				t.Latest = ytRel.Tag
				if t.Version != "" && t.Version != ytRel.Tag && t.Version < ytRel.Tag {
					t.Status = "old"
				}
			}
		}
		if ytRel != nil {
			if a := ytRel.asset(assetYtdlp); a != nil && t.Status != "ok" {
				size += a.Size
			}
		}
		if dp == "" {
			t.Note = "sem-deno"
			if t.Status == "ok" {
				t.Status = "old"
			}
			if denoRel != nil {
				if a := denoRel.asset(assetDeno); a != nil {
					size += a.Size
				}
			}
		}
		if size > 0 {
			t.Size = mb(size)
		}
		out = append(out, t)
	}

	// FFmpeg
	if b, ok := busy["ff"]; ok {
		out = append(out, *b)
	} else {
		t := Tool{ID: "ff"}
		if p := localTool("ffmpeg"); p != "" {
			t.Version = versionFFmpeg(p)
			t.Status, t.Source = "ok", "ztube"
			m := readManifest()
			if ffRel != nil && m.FFmpegDate != "" {
				if a := ffRel.asset(assetFF); a != nil {
					old, e1 := time.Parse(time.RFC3339, m.FFmpegDate)
					nu, e2 := time.Parse(time.RFC3339, a.Updated)
					if e1 == nil && e2 == nil && nu.Sub(old) > 30*24*time.Hour {
						t.Status = "old"
						t.Latest = nu.Format("02/01/2006")
					}
				}
			}
		} else if p := systemTool("ffmpeg"); p != "" {
			t.Version = versionFFmpeg(p)
			t.Status, t.Source = "ok", "sistema"
		} else {
			t.Status = "miss"
		}
		if ffRel != nil && t.Status != "ok" {
			if a := ffRel.asset(assetFF); a != nil {
				t.Size = mb(a.Size)
			}
		}
		out = append(out, t)
	}

	// WebView2 (vem com o Windows)
	out = append(out, Tool{ID: "wv", Status: "sys", Version: webview2Version(), Source: "windows"})
	return out
}

func refreshTools(remote bool) {
	emit("tools", checkTools(remote))
}

// ---------- instalação ----------

func setBusy(id string, pct float64, text string) {
	toolsMu.Lock()
	toolState[id] = &Tool{ID: id, Status: "busy", Pct: pct, Text: text}
	toolsMu.Unlock()
	emit("toolProgress", Tool{ID: id, Status: "busy", Pct: pct, Text: text})
}

func clearBusy(id string) {
	toolsMu.Lock()
	delete(toolState, id)
	toolsMu.Unlock()
}

var (
	installMu sync.Mutex
	reHex64   = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)
)

func installTool(id string) {
	installMu.Lock()
	defer installMu.Unlock()
	var err error
	switch id {
	case "yt":
		err = installYtdlp()
	case "ff":
		err = installFFmpeg()
	}
	clearBusy(id)
	if err != nil {
		emit("toolError", map[string]string{"id": id, "err": err.Error()})
	}
	refreshTools(false)
}

func ensureRel(key, repo, which string) (*ghRel, error) {
	toolsMu.Lock()
	r := latestRels[key]
	toolsMu.Unlock()
	if r != nil {
		return r, nil
	}
	r, err := ghRelease(repo, which)
	if err != nil {
		return nil, errors.New("Não consegui falar com o GitHub. Confira sua internet e tente de novo.")
	}
	toolsMu.Lock()
	latestRels[key] = r
	toolsMu.Unlock()
	return r, nil
}

func installYtdlp() error {
	setBusy("yt", 0, "Preparando…")
	rel, err := ensureRel("yt", repoYtdlp, "")
	if err != nil {
		return err
	}
	needYt := true
	if p := localTool("yt-dlp"); p != "" && versionYtdlp(p) == rel.Tag {
		needYt = false
	}
	needDeno := denoPath() == ""
	var denoRel *ghRel
	if lp := localTool("deno"); lp != "" || needDeno {
		denoRel, err = ensureRel("deno", repoDeno, "")
		if err != nil {
			return err
		}
		if lp != "" && "v"+versionDeno(lp) != denoRel.Tag {
			needDeno = true
		}
	}
	steps := 0
	if needYt {
		steps++
	}
	if needDeno {
		steps++
	}
	if steps == 0 {
		steps = 1
	}
	step := 0
	if needYt {
		a := rel.asset(assetYtdlp)
		if a == nil {
			return errors.New("Não encontrei o yt-dlp para Windows na página oficial.")
		}
		tmp, err := downloadVerified(rel, a, "yt", "yt-dlp", step, steps)
		if err != nil {
			return err
		}
		if err := replaceFile(tmp, filepath.Join(toolsDir, "yt-dlp.exe")); err != nil {
			return err
		}
		step++
	}
	if needDeno && denoRel != nil {
		a := denoRel.asset(assetDeno)
		if a == nil {
			return errors.New("Não encontrei o Deno para Windows na página oficial.")
		}
		tmp, err := downloadVerified(denoRel, a, "yt", "Deno", step, steps)
		if err != nil {
			return err
		}
		setBusy("yt", 99, "Extraindo o Deno…")
		err = extractFromZip(tmp, map[string]string{"deno.exe": "deno.exe"})
		os.Remove(tmp)
		if err != nil {
			return err
		}
	}
	return nil
}

func installFFmpeg() error {
	setBusy("ff", 0, "Preparando…")
	rel, err := ensureRel("ff", repoFFmpeg, "latest")
	if err != nil {
		return err
	}
	a := rel.asset(assetFF)
	if a == nil {
		return errors.New("Não encontrei o FFmpeg para Windows na página oficial.")
	}
	tmp, err := downloadVerified(rel, a, "ff", "FFmpeg", 0, 1)
	if err != nil {
		return err
	}
	setBusy("ff", 99, "Extraindo…")
	err = extractFromZip(tmp, map[string]string{"/bin/ffmpeg.exe": "ffmpeg.exe", "/bin/ffprobe.exe": "ffprobe.exe"})
	os.Remove(tmp)
	if err != nil {
		return err
	}
	m := readManifest()
	m.FFmpegDate = a.Updated
	writeManifest(m)
	return nil
}

// Hash SHA-256 esperado: do próprio GitHub ou do arquivo de checksums do projeto.
func expectedHash(rel *ghRel, a *ghAsset) string {
	if strings.HasPrefix(a.Digest, "sha256:") {
		return strings.ToLower(strings.TrimPrefix(a.Digest, "sha256:"))
	}
	for _, name := range []string{"SHA2-256SUMS", "checksums.sha256", a.Name + ".sha256sum", a.Name + ".sha256"} {
		ca := rel.asset(name)
		if ca == nil {
			continue
		}
		resp, err := ghGet(ca.URL)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(resp.Body)
		var only []string
		for sc.Scan() {
			line := sc.Text()
			hs := reHex64.FindAllString(line, -1)
			if len(hs) == 0 {
				continue
			}
			if strings.Contains(line, a.Name) {
				resp.Body.Close()
				return strings.ToLower(hs[0])
			}
			only = append(only, hs...)
		}
		resp.Body.Close()
		if len(only) == 1 {
			return strings.ToLower(only[0])
		}
	}
	return ""
}

func downloadVerified(rel *ghRel, a *ghAsset, id, label string, step, steps int) (string, error) {
	want := expectedHash(rel, a)
	if want == "" {
		return "", errors.New("Não consegui conferir o arquivo do " + label + ", então ele não foi instalado.")
	}
	resp, err := ghGet(a.URL)
	if err != nil {
		return "", errors.New("O download do " + label + " falhou. Confira sua internet e tente de novo.")
	}
	defer resp.Body.Close()
	tmp := filepath.Join(toolsDir, a.Name+".baixando")
	f, err := os.Create(tmp)
	if err != nil {
		return "", errors.New("Não consegui gravar na pasta de ferramentas.")
	}
	h := sha256.New()
	total := float64(a.Size)
	if resp.ContentLength > 0 {
		total = float64(resp.ContentLength)
	}
	var done float64
	last := time.Now()
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			f.Write(buf[:n])
			h.Write(buf[:n])
			done += float64(n)
			if time.Since(last) > 200*time.Millisecond {
				last = time.Now()
				p := 0.0
				if total > 0 {
					p = done / total
				}
				overall := (float64(step) + p) / float64(steps) * 95
				setBusy(id, overall, fmt.Sprintf("Baixando o %s do site oficial… %d%%", label, int(p*100)))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(tmp)
			return "", errors.New("O download do " + label + " foi interrompido. Tente de novo.")
		}
	}
	f.Close()
	setBusy(id, (float64(step)+1)/float64(steps)*95, "Conferindo se o arquivo chegou intacto…")
	if hex.EncodeToString(h.Sum(nil)) != want {
		os.Remove(tmp)
		return "", errors.New("O arquivo do " + label + " não bateu com o original e foi descartado. Tente de novo.")
	}
	return tmp, nil
}

func replaceFile(tmp, dst string) error {
	_ = os.Remove(dst + ".antigo")
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, dst+".antigo"); err != nil {
			os.Remove(tmp)
			return errors.New("A ferramenta está em uso. Espere os downloads terminarem e tente de novo.")
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Rename(dst+".antigo", dst)
		return errors.New("Não consegui instalar a ferramenta na pasta do ZTube.")
	}
	_ = os.Remove(dst + ".antigo")
	return nil
}

// Extrai arquivos do zip. A chave é o final do caminho dentro do zip.
func extractFromZip(zipPath string, want map[string]string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return errors.New("O arquivo baixado está corrompido. Tente de novo.")
	}
	defer zr.Close()
	found := 0
	for _, f := range zr.File {
		name := "/" + strings.ReplaceAll(f.Name, "\\", "/")
		for suffix, dst := range want {
			if !strings.HasSuffix(name, "/"+strings.TrimPrefix(suffix, "/")) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			tmp := filepath.Join(toolsDir, dst+".novo")
			out, err := os.Create(tmp)
			if err != nil {
				rc.Close()
				return errors.New("Não consegui gravar na pasta de ferramentas.")
			}
			_, err = io.Copy(out, rc)
			out.Close()
			rc.Close()
			if err != nil {
				os.Remove(tmp)
				return err
			}
			if err := replaceFile(tmp, filepath.Join(toolsDir, dst)); err != nil {
				return err
			}
			found++
		}
	}
	if found < len(want) {
		return errors.New("O pacote baixado não tinha todos os arquivos esperados.")
	}
	return nil
}
