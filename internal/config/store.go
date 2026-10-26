package config

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store 负责 settings.json 的读写。
// 绿色版原则：只碰 data 目录；写入用临时文件 + rename 原子替换；损坏自动备份回落默认值。
type Store struct {
	mu      sync.RWMutex
	dir     string
	path    string
	cur     Settings
	key     []byte // API Key 混淆密钥（机器派生，非加密强度，仅避免明文落盘）
	lastErr error
}

// NewStore 创建并加载存储。dir 不存在时自动创建。
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("定位 exe: %w", err)
		}
		dir = filepath.Join(filepath.Dir(exe), "data")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建数据目录 %s: %w", dir, err)
	}
	s := &Store{
		dir:  dir,
		path: filepath.Join(dir, "settings.json"),
		key:  machineKey(),
	}
	if err := s.load(); err != nil {
		s.lastErr = err
	}
	return s, nil
}

// Dir 返回数据目录。
func (s *Store) Dir() string { return s.dir }

// Path 返回 settings.json 路径。
func (s *Store) Path() string { return s.path }

// LastError 返回上次加载时的错误（UI 可提示「配置已重置」）。
func (s *Store) LastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

// machineKey 由机器标识与固定盐派生混淆密钥；失败时退回固定值（功能不受影响）。
// 只依赖主机名、不掺入数据目录：绿色版整目录搬走后 Key 仍可解出。
func machineKey() []byte {
	host, _ := os.Hostname()
	sum := sha256.Sum256([]byte("svg-master/v2|" + host))
	return sum[:]
}

func (s *Store) xorKey(v string) string {
	plain := decodeKey(v, s.key)
	if plain == "" {
		return ""
	}
	buf := []byte(plain)
	for i := range buf {
		buf[i] ^= s.key[i%len(s.key)]
	}
	return "enc:" + base64.StdEncoding.EncodeToString(buf)
}

// decodeKey 用主密钥解混淆；老版本（密钥掺入数据目录）写下的密文解出乱码时，
// 依次尝试历史密钥，保证升级后 Key 不丢。
func decodeKey(v string, key []byte) string {
	if !strings.HasPrefix(v, "enc:") {
		return v
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "enc:"))
	if err != nil {
		return ""
	}
	for _, k := range append([][]byte{key}, legacyKeys()...) {
		plain := xorBytes(raw, k)
		if printable(plain) {
			return plain
		}
	}
	return xorBytes(raw, key)
}

func xorBytes(raw []byte, key []byte) string {
	if len(key) == 0 {
		return ""
	}
	buf := make([]byte, len(raw))
	copy(buf, raw)
	for i := range buf {
		buf[i] ^= key[i%len(key)]
	}
	return string(buf)
}

// printable 判断解出的明文像不像一个真实 Key（可见 ASCII，无控制字符）。
func printable(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r > 0xff {
			return false
		}
	}
	return true
}

// legacyKeys 返回历史版本的派生密钥（v1 掺入数据目录，与当前目录绑定）。
func legacyKeys() [][]byte {
	host, _ := os.Hostname()
	if host == "" {
		return nil
	}
	var out [][]byte
	for _, dir := range legacyDirs() {
		sum := sha256.Sum256([]byte("svg-master/v1|" + host + "|" + dir))
		out = append(out, sum[:])
	}
	return out
}

// legacyDirs 是历史版本可能用过的数据目录写法。
func legacyDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "data"))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "data"))
	}
	return dirs
}

// Settings 返回当前设置的副本（Key 已解码为明文）。
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.cur
	out.Providers = make([]Provider, len(s.cur.Providers))
	copy(out.Providers, s.cur.Providers)
	for i := range out.Providers {
		out.Providers[i].APIKey = decodeKey(out.Providers[i].APIKey, s.key)
	}
	return out
}

// SettingsMasked 返回设置副本，API Key 打码（只留前 4 后 4）。
func (s *Store) SettingsMasked() Settings {
	out := s.Settings()
	for i := range out.Providers {
		out.Providers[i].APIKey = MaskKey(out.Providers[i].APIKey)
	}
	return out
}

// MaskKey 打码：sk-abcdefghij → sk-a****hij。
func MaskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "****"
	}
	return k[:4] + "****" + k[len(k)-4:]
}

// Save 校验后持久化。入参 Key 为明文（可为打码值，表示「保持不变」）。
func (s *Store) Save(in Settings) error {
	norm, err := Normalize(in, s.Settings())
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range norm.Providers {
		norm.Providers[i].APIKey = s.xorKey(norm.Providers[i].APIKey)
	}
	if err := s.write(norm); err != nil {
		return err
	}
	norm = reDecode(norm, s.key)
	s.cur = norm
	s.lastErr = nil
	return nil
}

func reDecode(in Settings, key []byte) Settings {
	out := in
	out.Providers = make([]Provider, len(in.Providers))
	copy(out.Providers, in.Providers)
	for i := range out.Providers {
		out.Providers[i].APIKey = decodeKey(out.Providers[i].APIKey, key)
	}
	return out
}

// Normalize 修正非法值并补齐缺省；prev 用于把打码 Key 还原成原值。
func Normalize(in Settings, prev Settings) (Settings, error) {
	out := in
	out.Version = SettingsVersion
	if len(out.Providers) == 0 {
		out.Providers = DefaultSettings().Providers
	}
	seen := map[string]bool{}
	for i := range out.Providers {
		p := &out.Providers[i]
		if p.ID == "" || seen[p.ID] {
			p.ID = fmt.Sprintf("p%d", time.Now().UnixNano()+int64(i))
		}
		seen[p.ID] = true
		if p.AuthMode == "chatgpt" {
			p.Protocol = ProtoOpenAIResp
			p.BaseURL = "https://api.openai.com/v1"
			p.APIKey = ""
		} else {
			p.AuthMode = ""
		}
		if _, ok := MetaOf(p.Protocol); !ok {
			p.Protocol = ProtoOpenAIChat
		}
		meta, _ := MetaOf(p.Protocol)
		// Base URL 不再要求用户手写 /v1：这里统一归一（补协议、削斜杠、按协议补版本段）。
		// 空值回落到协议默认值。
		p.BaseURL = CanonicalBaseURL(p.Protocol, p.BaseURL)
		if p.BaseURL == "" {
			p.BaseURL = strings.TrimRight(meta.BaseURL, "/")
		}
		if !strings.HasPrefix(p.BaseURL, "http://") && !strings.HasPrefix(p.BaseURL, "https://") {
			return out, fmt.Errorf("供应商 %q 的 Base URL 必须以 http:// 或 https:// 开头", p.Name)
		}
		if strings.Contains(p.APIKey, "****") {
			// 前端回传的是打码值 → 沿用原 Key
			if old := findProvider(prev, p.ID); old != nil {
				p.APIKey = old.APIKey
			} else {
				p.APIKey = ""
			}
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			p.Name = meta.Label
		}
		for j := range p.Models {
			p.Models[j].ID = strings.TrimSpace(p.Models[j].ID)
			if p.Models[j].Name == "" {
				p.Models[j].Name = p.Models[j].ID
			}
		}
	}
	if !seen[out.ActiveProviderID] {
		out.ActiveProviderID = out.Providers[0].ID
	}
	switch out.Thinking {
	case "off", "low", "medium", "high":
	case "xhigh", "max":
		// xhigh / max 只在用户显式勾了「扩展思考」时生效
		if !out.ExtendedThinking {
			out.Thinking = "high"
		}
	default:
		out.Thinking = "off"
	}
	if out.Agent.MaxSteps <= 0 || out.Agent.MaxSteps > 200 {
		out.Agent.MaxSteps = 40
	}
	if out.Agent.CompressAt <= 0 || out.Agent.CompressAt > 95 {
		out.Agent.CompressAt = 75
	}
	// 生成方式：合法值原样保留，空/非法值归一，并同步派生的 MultiStep。
	out.Agent = NormalizeGenMode(out.Agent)
	if out.Layers.PanelHeight < 120 || out.Layers.PanelHeight > 900 {
		out.Layers.PanelHeight = 220
	}
	if out.Project.Mode != ModeProject {
		out.Project.Mode = ModeQuick
	}
	if len(out.Project.Recent) > 12 {
		out.Project.Recent = out.Project.Recent[:12]
	}
	if out.Canvas.Width <= 0 || out.Canvas.Height <= 0 {
		out.Canvas = DefaultSettings().Canvas
	}
	if out.Export.Scale <= 0 || out.Export.Scale > 8 {
		out.Export.Scale = 2
	}
	return out, nil
}

func findProvider(s Settings, id string) *Provider {
	for i := range s.Providers {
		if s.Providers[i].ID == id {
			return &s.Providers[i]
		}
	}
	return nil
}

func (s *Store) write(v Settings) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("写入临时配置: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		// Windows 上 rename 到已存在文件通常可覆盖；失败则先删再试
		if rmErr := os.Remove(s.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return fmt.Errorf("替换配置: %w", err)
		}
		if err := os.Rename(tmp, s.path); err != nil {
			return fmt.Errorf("替换配置: %w", err)
		}
	}
	return nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		def := DefaultSettings()
		if err := s.write(def); err != nil {
			return err
		}
		s.cur = def
		return nil
	}
	if err != nil {
		s.cur = DefaultSettings()
		return fmt.Errorf("读取配置: %w", err)
	}
	var v Settings
	if err := json.Unmarshal(raw, &v); err != nil {
		// 损坏：备份后回落默认值
		bak := fmt.Sprintf("%s.bak.%d", s.path, time.Now().Unix())
		_ = os.WriteFile(bak, raw, 0o644)
		def := DefaultSettings()
		_ = s.write(def)
		s.cur = def
		return fmt.Errorf("配置损坏，已备份为 %s 并重置默认值", filepath.Base(bak))
	}
	v = migrate(v)
	v = MigrateTo3(v)
	v = MigrateTo4(v)
	norm, err := Normalize(v, DefaultSettings())
	if err != nil {
		s.cur = DefaultSettings()
		return err
	}
	norm = reDecode(norm, s.key)
	s.cur = norm
	return nil
}

// migrate 处理历史版本（v1 单供应商 → v2 多供应商）。
func migrate(v Settings) Settings {
	if v.Version >= 2 {
		return v
	}
	if len(v.Providers) == 0 {
		v = DefaultSettings()
	}
	v.Version = 2
	return v
}

// MigrateTo3 把 v2 配置升到 v3：补上 MultiStep / Layers / Project 三组新字段的默认值。
// 只填空缺，不覆盖用户已改的值，因此可以在每次加载时幂等执行。
func MigrateTo3(v Settings) Settings {
	def := DefaultSettings()
	if v.Version < 3 {
		v.Version = 3
		// v2 没有 MultiStep 概念：升级后默认打开（用户可在右栏关掉）。
		v.Agent.MultiStep = true
	}
	if v.Layers.PanelHeight < 120 || v.Layers.PanelHeight > 900 {
		v.Layers.PanelHeight = def.Layers.PanelHeight
	}
	if v.Project.Mode != ModeProject {
		v.Project.Mode = ModeQuick
	}
	return v
}

// MigrateTo4 把 v3 配置升到 v4：引入生成方式三级开关。
//
// v3 只有布尔型 MultiStep，没有 genMode；这里按 MultiStep 推断一次
// （true → layers，false → quick），之后的加载就是幂等的字段归一。
func MigrateTo4(v Settings) Settings {
	if v.Version < 4 {
		v.Version = SettingsVersion
	}
	// 无论版本号如何，都做一次字段归一：手改坏的 genMode 也能被纠正，
	// 且 MultiStep 派生值始终与 genMode 一致。
	v.Agent = NormalizeGenMode(v.Agent)
	return v
}
