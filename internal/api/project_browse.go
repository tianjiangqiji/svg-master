package api

// 系统文件夹选择器。
//
//	POST /api/project/browse  {"title":"...", "initial":"..."}  -> {"ok":true,"path":"D:\\..."}
//
// 内置窗口（WebView2）模式下，浏览器原生的文件夹选择在 WebView 里不可用，
// 这里直接调宿主系统的对话框；-web 调试模式（系统浏览器）回落成 false，
// 前端据此改用 <input webkitdirectory> 选目录。

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// pickFolderResult 选择结果；path 为空表示用户取消。
type pickFolderResult struct {
	Path     string `json:"path"`
	Fallback bool   `json:"fallback"` // true：宿主无法弹窗，请前端自行选择
}

func (s *Server) handleProjectBrowse(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title   string `json:"title"`
		Initial string `json:"initial"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)

	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "请用 POST")
		return
	}
	if !s.desktop {
		writeJSON(w, pickFolderResult{Fallback: true})
		return
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "选择文件夹"
	}
	title = strings.NewReplacer("'", "''").Replace(title)
	initial := strings.TrimSpace(in.Initial)
	if initial != "" {
		initial = strings.NewReplacer("'", "''").Replace(initial)
	}

	script := `Add-Type -AssemblyName System.Windows.Forms;` +
		`$d = New-Object System.Windows.Forms.FolderBrowserDialog;` +
		`$d.Description = '` + title + `';` +
		`$d.ShowNewFolderButton = $true;`
	if initial != "" {
		script += `try { $d.SelectedPath = '` + initial + `' } catch { };`
	}
	// 必须在 STA 线程上开对话框，否则控件会抛异常。
	script += `$f = New-Object System.Windows.Forms.Form;` +
		`$f.TopMost = $true; $f.ShowInTaskbar = $false;` +
		`$r = $d.ShowDialog($f); $f.Dispose();` +
		`if ($r -eq 'OK') { [Console]::Out.Write($d.SelectedPath) }`

	out, err := exec.Command("powershell", "-NoProfile", "-STA", "-Command", script).Output()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "打开文件夹选择器失败: "+err.Error())
		return
	}
	path := strings.TrimSpace(string(out))
	if path != "" {
		if _, statErr := os.Stat(path); statErr == nil {
			abs, aerr := filepath.Abs(path)
			if aerr == nil {
				path = abs
			}
		}
	}
	writeJSON(w, pickFolderResult{Path: path})
}
