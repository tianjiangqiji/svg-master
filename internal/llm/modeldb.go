package llm

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"svg-master/internal/config"
)

// modelDB 从 models.dev（开源模型元数据库，https://github.com/anomalyco/models.dev）
// 拉取全量模型规格（上下文窗口 / 最大输出 / 推理 / 视觉），缓存到 data 目录，
// 能力推断优先查库，查不到再回落内置规则表。
// 数据结构（v актуальной версии api.json）：
//   { "<provider>": { "models": { "<model-id>": {
//       "limit": {"context": N, "output": N},
//       "reasoning": bool,
//       "modalities": {"input": ["text","image",...]}, ... } } } }

const (
	modelDBURL      = "https://models.dev/api.json"
	modelDBFileName = "modelsdb.json"
	modelDBMaxAge   = 7 * 24 * time.Hour // 超过 7 天尝试刷新，失败沿用旧缓存
)

type modelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

type modelEntry struct {
	Limit      *modelLimit `json:"limit"`
	Reasoning  bool        `json:"reasoning"`
	Modalities *struct {
		Input []string `json:"input"`
	} `json:"modalities"`
}

type modelDBFile struct {
	FetchedAt time.Time              `json:"fetchedAt"`
	Providers map[string]modelEntry2 `json:"providers"`
}

// provider 节点：models.dev 顶层是 {provider: {models: {...}}}，只保留需要的字段
type providerNode struct {
	Models map[string]modelEntry `json:"models"`
}

// modelEntry2 = providerNode + 兼容序列化别名
type modelEntry2 = providerNode

var (
	modelDBMu       sync.RWMutex
	modelDB         map[string]map[string]modelEntry // providerKey(lower) -> modelID(lower) -> entry
	modelDBLoaded   bool
	modelDBFetching sync.Once
)

// ModelDBDir 供 app 层注入数据目录（必须在使用前调用；空则退化为纯内置规则）。
var modelDBDir string

func SetModelDBDir(dir string) { modelDBDir = dir }

// loadModelDB 从磁盘缓存加载（无网络）。
func loadModelDB() {
	if modelDBLoaded {
		return
	}
	modelDBLoaded = true
	if modelDBDir == "" {
		return
	}
	raw, err := os.ReadFile(filepath.Join(modelDBDir, modelDBFileName))
	if err != nil {
		return
	}
	var f modelDBFile
	if json.Unmarshal(raw, &f) != nil || len(f.Providers) == 0 {
		return
	}
	m := make(map[string]map[string]modelEntry, len(f.Providers))
	for prov, node := range f.Providers {
		if len(node.Models) == 0 {
			continue
		}
		key := strings.ToLower(prov)
		mm := make(map[string]modelEntry, len(node.Models))
		for id, e := range node.Models {
			mm[strings.ToLower(id)] = e
		}
		m[key] = mm
	}
	modelDBMu.Lock()
	modelDB = m
	modelDBMu.Unlock()
}

// saveModelDB 落盘精简缓存。
func saveModelDB(providers map[string]providerNode) {
	if modelDBDir == "" {
		return
	}
	f := modelDBFile{FetchedAt: time.Now(), Providers: providers}
	raw, err := json.Marshal(&f)
	if err != nil {
		return
	}
	p := filepath.Join(modelDBDir, modelDBFileName)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// fetchModelDB 拉取 models.dev 并构建内存索引（后台静默，失败不影响使用）。
func fetchModelDB() {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(modelDBURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var raw map[string]providerNode
	if json.NewDecoder(resp.Body).Decode(&raw) != nil {
		return
	}
	m := make(map[string]map[string]modelEntry, len(raw))
	for prov, node := range raw {
		if len(node.Models) == 0 {
			continue
		}
		key := strings.ToLower(prov)
		mm := make(map[string]modelEntry, len(node.Models))
		for id, e := range node.Models {
			mm[strings.ToLower(id)] = e
		}
		m[key] = mm
	}
	modelDBMu.Lock()
	modelDB = m
	modelDBMu.Unlock()
	saveModelDB(raw)
	log.Printf("[models] 能力库已从 models.dev 更新（%d 个供应商）", len(m))
}

// ensureModelDB 首次使用时同步加载磁盘缓存（快，~1MB JSON），
// 缓存缺失/过期时再后台联网刷新，不阻塞调用方。
func ensureModelDB() {
	modelDBFetching.Do(func() {
		loadModelDB() // 同步：保证后续 dbLookup 一定能看到库
		go func() {
			// 缓存新鲜（<7 天）就不联网
			if modelDBDir != "" {
				if st, err := os.Stat(filepath.Join(modelDBDir, modelDBFileName)); err == nil {
					if time.Since(st.ModTime()) < modelDBMaxAge && len(modelDB) > 0 {
						return
					}
				}
			}
			if len(modelDB) == 0 {
				loadModelDB() // 双保险
				if len(modelDB) == 0 {
					fetchModelDB()
				}
			}
		}()
	})
}

// variantSuffixRe 剥掉网关加在模型 id 上的常见变体后缀，
// 让 "deepseek-v4.1-flash-1017-g" / "glm-5.2-1105" 这类改名也能命中库。
var variantSuffixRe = regexp.MustCompile(`(-g$|-\d{3,4}$|-(?:latest|free|pro|plus)$)`)

// collectHits 收集库内所有与 id（或其变体剥离形）匹配的条目。
func collectHits(db map[string]map[string]modelEntry, lid string) []modelEntry {
	var hits []modelEntry
	if i := strings.IndexByte(lid, '/'); i > 0 {
		mm := db[lid[:i]]
		if mm == nil {
			return nil
		}
		if e, ok := mm[lid[i+1:]]; ok {
			return []modelEntry{e}
		}
		stripped := variantSuffixRe.ReplaceAllString(lid[i+1:], "")
		if stripped != "" && stripped != lid[i+1:] {
			if e, ok := mm[stripped]; ok {
				return []modelEntry{e}
			}
		}
		return nil
	}
	for _, mm := range db {
		if e, ok := mm[lid]; ok {
			hits = append(hits, e)
		}
	}
	if len(hits) == 0 {
		if stripped := variantSuffixRe.ReplaceAllString(lid, ""); stripped != lid && stripped != "" {
			for _, mm := range db {
				if e, ok := mm[stripped]; ok {
					hits = append(hits, e)
				}
			}
		}
	}
	return hits
}

// majorityInt 取众数（平票取较大值），保证同 id 多 provider 条目不一致时结果确定。
func majorityInt(vals []int) int {
	count := make(map[int]int, len(vals))
	for _, v := range vals {
		count[v]++
	}
	best, bestN := 0, -1
	for v, n := range count {
		if n > bestN || (n == bestN && v > best) {
			best, bestN = v, n
		}
	}
	return best
}

// majorityBool 取多数派（平票倾向 true：能力有比没有好）。
func majorityBool(vals []bool) bool {
	n := 0
	for _, v := range vals {
		if v {
			n++
		}
	}
	return n*2 >= len(vals)
}

// dbLookup 聚合库内全部命中条目：context/output 取众数（平票取大），
// reasoning/vision 取多数派（平票取 true）。跨 provider 数据不一致时结果
// 确定且与主流规格一致。
func dbLookup(id string) (modelEntry, bool) {
	modelDBMu.RLock()
	db := modelDB
	modelDBMu.RUnlock()
	if len(db) == 0 {
		return modelEntry{}, false
	}
	hits := collectHits(db, strings.ToLower(id))
	if len(hits) == 0 {
		return modelEntry{}, false
	}
	ctxs := make([]int, 0, len(hits))
	outs := make([]int, 0, len(hits))
	rea := make([]bool, 0, len(hits))
	vis := make([]bool, 0, len(hits))
	for _, e := range hits {
		if e.Limit != nil {
			ctxs = append(ctxs, e.Limit.Context)
			outs = append(outs, e.Limit.Output)
		}
		rea = append(rea, e.Reasoning)
		v := false
		if e.Modalities != nil {
			for _, m := range e.Modalities.Input {
				if m == "image" {
					v = true
					break
				}
			}
		}
		vis = append(vis, v)
	}
	if len(ctxs) == 0 {
		return modelEntry{}, false
	}
	ctx := majorityInt(ctxs)
	if ctx <= 0 {
		return modelEntry{}, false
	}
	out := majorityInt(outs)
	if out <= 0 {
		out = 16384
	}
	reason := majorityBool(rea)
	e := modelEntry{Limit: &modelLimit{Context: ctx, Output: out}, Reasoning: reason}
	if majorityBool(vis) {
		e.Modalities = &struct {
			Input []string `json:"input"`
		}{Input: []string{"text", "image"}}
	}
	return e, true
}

// entryToCaps 把库条目转成 ModelCaps。
func entryToCaps(e modelEntry) (config.ModelCaps, bool) {
	if e.Limit == nil || e.Limit.Context <= 0 {
		return config.ModelCaps{}, false
	}
	out := e.Limit.Output
	if out <= 0 {
		out = 16384
	}
	caps := config.ModelCaps{ContextWindow: e.Limit.Context, MaxTokens: out}
	caps.Reasoning = e.Reasoning
	if e.Modalities != nil {
		for _, m := range e.Modalities.Input {
			if m == "image" {
				caps.Vision = true
				break
			}
		}
	}
	return caps, true
}

// dbLookupCaps 查库并转 caps 的组合快捷方式。
func dbLookupCaps(modelID string) (config.ModelCaps, bool) {
	ensureModelDB()
	e, ok := dbLookup(modelID)
	if !ok {
		return config.ModelCaps{}, false
	}
	return entryToCaps(e)
}
