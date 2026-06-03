// Package config 加载并解析 LLM 服务端 yaml 配置文件。
//
// Loader 在 viper 之上封装了一套常用模式：
//   - Unmarshal 到现有的 [LLMConfig] 结构体（通过 mapstructure hook 复用 yaml tag）
//   - 文件不存在时按需写一份带默认值的模板
//   - 可选环境变量覆盖（按 KeyPrefix 命名）
//   - 可选命令行 flag 绑定（pflag）
//   - 可选热加载（fsnotify），回调签名 func(*LLMConfig)
//
// 调用方只需要：
//
//	loader, err := config.Load("config.yaml", config.Options{Watch: true, OnConfigChange: cb})
//	if err != nil { ... }
//	defer loader.Close()
//	cfg := loader.Config()
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Options 控制 Load 的行为。所有字段零值都安全。
type Options struct {
	// EnvPrefix 启用环境变量覆盖；非空前缀时生效，
	// 形如 PREFIX_BORING_APIKEY 会覆盖 yaml 里的 apiKey。
	EnvPrefix string

	// FlagSet 启用命令行 flag 覆盖；非 nil 时生效。
	// 约定：--api-key / --sdk / --base-url / --model-id / --provider 等"扁平"key 与
	// [LLMConfig] 字段一一对应。复杂字段（model）走 --model-id 单独绑定。
	FlagSet *pflag.FlagSet

	// WriteTemplate 在配置文件不存在时是否写一份带默认值的模板。
	// 默认 false（调用方应自己准备文件）。
	WriteTemplate bool

	// Watch 启用热加载；需要 OnConfigChange 才会触发回调。
	Watch bool

	// OnConfigChange 热加载回调；文件变更后用最新内容重新解析再调用。
	// Watch=true 时有效。
	OnConfigChange func(*LLMConfig)
}

// Loader 是 Load 返回的句柄。线程安全：Config 永远返回当前已解析值，
// 热加载会原子地替换内部指针。
type Loader struct {
	mu     sync.RWMutex
	cfg    *LLMConfig
	loader *viper.Viper

	// 热加载相关资源
	watcher  *fsnotify.Watcher
	closeCh  chan struct{}
	closeOne sync.Once
}

// Config 返回当前解析后的配置。每次热加载会原子替换内部指针，
// 因此调用方在拿到 *LLMConfig 后应避免长期持有（或者在使用前再次调用 Config）。
func (l *Loader) Config() *LLMConfig {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cfg
}

// Viper 暴露底层 viper 实例，给需要直接 GetXxx 的特殊场景使用。
// 一般业务代码应使用 [Loader.Config]。
func (l *Loader) Viper() *viper.Viper {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.loader
}

// Close 停止热加载并释放 fsnotify 资源。多次调用安全。
func (l *Loader) Close() error {
	var err error
	l.closeOne.Do(func() {
		close(l.closeCh)
		if l.watcher != nil {
			err = l.watcher.Close()
		}
	})
	return err
}

// Load 解析 path 指定的 yaml 配置文件并返回 [Loader]。
//
// 行为：
//   - 文件不存在时直接报错（除非 opts.WriteTemplate=true，会先写一份默认模板）
//   - 解析时把 yaml tag 适配给 mapstructure，并自动把 string 转成 url.URL
//   - opts 启用的能力（env / flag / watch）会按顺序生效：watch 优先级最低，
//     flag 高于 env，env 高于文件
func Load(path string, opts Options) (*Loader, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: resolve path: %w", err)
	}

	if _, statErr := os.Stat(abs); errors.Is(statErr, os.ErrNotExist) {
		if !opts.WriteTemplate {
			return nil, fmt.Errorf("config: %s not found (set Options.WriteTemplate to auto-generate): %w", abs, os.ErrNotExist)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, fmt.Errorf("config: mkdir %s: %w", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(defaultTemplateYAML), 0o644); err != nil {
			return nil, fmt.Errorf("config: write template: %w", err)
		}
	}

	v := viper.New()
	v.SetConfigFile(abs)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("config: read %s: %w", abs, err)
	}

	// 在 SetDefault 之前快照 yaml 显式 key 集合。
	// [resolveProviderDefaults] 需要可靠区分「yaml/env/flag 显式输入」与「程序 default 兜底」，
	// viper 的 IsSet 在 SetDefault 后会把 default 误判为「已 set」，所以不能直接用。
	yamlKeys := flattenViperKeys(v.AllSettings())

	// 默认值：即使 yaml 里没写也能给出合理兜底
	v.SetDefault("provider", "")
	v.SetDefault("baseUrl", "")
	v.SetDefault("apiKey", "")
	v.SetDefault("sdk", SdkOpenAIChat)
	v.SetDefault("model.name", "")
	v.SetDefault("model.id", "")
	v.SetDefault("model.maxResponse", 0)
	v.SetDefault("model.maxContext", 0)

	if opts.EnvPrefix != "" {
		v.SetEnvPrefix(opts.EnvPrefix)
		v.AutomaticEnv()
		// url.URL 走 string 中间层（key 是 baseUrl，下划线是 viper 默认替换策略）
		_ = v.BindEnv("provider")
		_ = v.BindEnv("baseUrl")
		_ = v.BindEnv("apiKey")
		_ = v.BindEnv("sdk")
		_ = v.BindEnv("model.id")
	}

	if opts.FlagSet != nil {
		bindFlags(v, opts.FlagSet)
	}

	// 第一次解析
	cfg, err := unmarshalLLMConfig(v)
	if err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	// Provider 预设下填默认值 + 校验 sdk 一致性。失败即 fail-fast。
	if err := resolveProviderDefaults(v, yamlKeys, cfg); err != nil {
		return nil, fmt.Errorf("config: resolve provider defaults: %w", err)
	}

	l := &Loader{
		cfg:     cfg,
		loader:  v,
		closeCh: make(chan struct{}),
	}

	if opts.Watch {
		if opts.OnConfigChange == nil {
			return nil, fmt.Errorf("config: Options.Watch=true requires OnConfigChange")
		}
		if err := l.startWatch(abs, opts.OnConfigChange); err != nil {
			_ = l.Close()
			return nil, err
		}
	}

	return l, nil
}

// unmarshalLLMConfig 把 viper 当前状态反序列化到 [LLMConfig]。
// 关键点：把 mapstructure 的默认 tag 切换成 yaml，复用现有 struct 的 yaml tag。
func unmarshalLLMConfig(v *viper.Viper) (*LLMConfig, error) {
	var cfg LLMConfig
	if err := v.Unmarshal(&cfg, func(dc *mapstructure.DecoderConfig) {
		dc.TagName = "yaml"
		dc.DecodeHook = mapstructure.ComposeDecodeHookFunc(
			stringToURLHook(),
			dc.DecodeHook,
		)
	}); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// resolveProviderDefaults 用 [LLMConfig.Provider] 填充 baseUrl / sdk / model.id 的缺省值，
// 并校验显式指定的 sdk 是否落在 provider 允许的协议列表内。
//
// yamlKeys 是从 yaml 原始内容拍平的扁平 key 集合 (例如 "model.id")，用于可靠区分
// 「用户显式输入」与「程序 default 兜底」——viper 的 IsSet 在 SetDefault 之后会把 default
// 误判为「已 set」，所以这里直接用 yaml 显式 key 集合做判据。
//
// Provider 为空时（未启用 provider 字段）函数 no-op，
// 保留原有"通过 sdk + baseUrl 手写"的行为。fail-fast 错误会以 config: 前缀返回。
//
// 内部通过 [getProviderDefaultConfig] / [getAllowedSdks] 获取 provider 的默认配置；
// 这些值由 provider 包在其 init() 中通过 [RegisterProviderDefaults] 注册，
// 从而避免 config → provider 的导入循环。
func resolveProviderDefaults(_ *viper.Viper, yamlKeys map[string]bool, cfg *LLMConfig) error {
	if cfg.Provider == "" {
		return nil
	}

	defaultCfg, ok := getProviderDefaultConfig(cfg.Provider)
	if !ok {
		return fmt.Errorf("未识别的 provider %q（可用: openai / anthropic / deepseek）", cfg.Provider)
	}

	// 1. baseUrl：未显式配置时回退到 provider 的官方默认。Host 为零值即视为"未配置"。
	if cfg.BaseURL.Host == "" {
		cfg.BaseURL = defaultCfg.BaseURL
	}

	// 2. sdk：未显式配置时回退到 provider 的默认协议；显式配置时必须落在 AllowedSdks 内，否则 fail-fast。
	//    yamlKeys 是"yaml 显式"集合；env / flag 覆盖当前不参与显式判定（直接走 viper 已解析值）。
	if !yamlKeys["sdk"] {
		cfg.Sdk = defaultCfg.Sdk
	} else {
		allowed, ok := getAllowedSdks(cfg.Provider)
		if !ok {
			return fmt.Errorf("未识别的 provider %q（可用: openai / anthropic / deepseek）", cfg.Provider)
		}
		if !slices.Contains(allowed, cfg.Sdk) {
			return fmt.Errorf("provider %q 不允许 sdk=%q（允许: %v）", cfg.Provider, cfg.Sdk, allowed)
		}
	}

	// 3. model.id：未显式配置时回退到 provider 的默认模型。
	if cfg.Model.ID == "" {
		cfg.Model.ID = defaultCfg.Model.ID
	}

	return nil
}

// ── Provider 默认配置注册表 ──────────────────────────────────────────
//
// provider 包在其 init() 中通过 [RegisterProviderDefaults] 注册各 provider
// 的默认配置及允许的 Sdk 列表，[resolveProviderDefaults] 通过
// [getProviderDefaultConfig] / [getAllowedSdks] 读取。
//
// 这是为了打破 config ↔ provider 的导入循环：config 包不 import provider，
// 而是由 provider 包主动注册自身信息到 config 包的内部注册表。
// ─────────────────────────────────────────────────────────────────────

var (
	providerDefaultMu     sync.RWMutex
	providerDefaultConfig = map[Provider]LLMConfig{}
	providerAllowedSdks   = map[Provider][]Sdk{}
)

// RegisterProviderDefaults 供 provider 包注册该 provider 的默认 LLMConfig
// 及允许的 Sdk 列表。多次注册同一 provider 会覆盖前值（最后注册胜出）。
//
// 应在 init() 中调用。
func RegisterProviderDefaults(p Provider, defaultCfg LLMConfig, allowed []Sdk) {
	providerDefaultMu.Lock()
	defer providerDefaultMu.Unlock()
	providerDefaultConfig[p] = defaultCfg
	providerAllowedSdks[p] = allowed
}

func getProviderDefaultConfig(p Provider) (LLMConfig, bool) {
	providerDefaultMu.RLock()
	defer providerDefaultMu.RUnlock()
	cfg, ok := providerDefaultConfig[p]
	return cfg, ok
}

func getAllowedSdks(p Provider) ([]Sdk, bool) {
	providerDefaultMu.RLock()
	defer providerDefaultMu.RUnlock()
	allowed, ok := providerAllowedSdks[p]
	return allowed, ok
}

// flattenViperKeys 把 viper 的 [AllSettings] map 拍平为扁平 key 集合（用 "." 分隔嵌套层）。
// 仅用于在 SetDefault / BindEnv / BindPFlag 之前快照 yaml 显式包含的 key，
// 供 [resolveProviderDefaults] 判断「yaml 显式」与「程序兜底」用。
func flattenViperKeys(m map[string]any) map[string]bool {
	out := map[string]bool{}
	flattenInto(m, "", out)
	return out
}

func flattenInto(m map[string]any, prefix string, out map[string]bool) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		out[key] = true
		if vm, ok := v.(map[string]any); ok {
			flattenInto(vm, key, out)
		}
	}
}

// stringToURLHook 在 mapstructure 解析时把 string 喂给 url.URL 字段。
// yaml 里 baseUrl 是 string（人类可写），结构体里是 url.URL（程序可操作）。
func stringToURLHook() mapstructure.DecodeHookFunc {
	urlType := reflect.TypeFor[url.URL]()
	return func(from, to reflect.Type, data any) (any, error) {
		if to != urlType {
			return data, nil
		}
		switch v := data.(type) {
		case string:
			if v == "" {
				return url.URL{}, nil
			}
			u, err := url.Parse(v)
			if err != nil {
				return nil, fmt.Errorf("parse url %q: %w", v, err)
			}
			return *u, nil
		case nil:
			return url.URL{}, nil
		default:
			return data, nil
		}
	}
}

// bindFlags 把 pflag 绑到 viper key。flag 命名约定 kebab-case。
func bindFlags(v *viper.Viper, fs *pflag.FlagSet) {
	for _, b := range []struct {
		key  string
		name string
		help string
	}{
		{"provider", "provider", "LLM provider 内置预设: openai / anthropic / deepseek"},
		{"baseUrl", "base-url", "LLM provider base URL"},
		{"apiKey", "api-key", "LLM provider API key"},
		{"sdk", "sdk", "SDK 协议: openai-chat / openai-response / anthropic-message / deepseek"},
		{"model.id", "model-id", "Model ID (如 gpt-4o)"},
	} {
		// 如果 flag 没显式设置，BindPFlag 不会覆盖 viper 已有的值（来自 yaml / env），
		// 满足 flag > env > file 的优先级。
		_ = v.BindPFlag(b.key, fs.Lookup(b.name))
	}
}

// startWatch 启动 fsnotify 监听配置文件；触发后用最新内容重新解析再调用 onChange。
// 监听目录而不是单文件：很多编辑器（vim / 一些 IDE）的保存是"rename 临时文件 → 覆盖"，
// 直接监听文件会漏掉事件；监听目录能稳定捕获 create / write / rename。
func (l *Loader) startWatch(path string, onChange func(*LLMConfig)) error {
	dir := filepath.Dir(path)
	filename := filepath.Base(path)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("config: watch: %w", err)
	}
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return fmt.Errorf("config: watch dir %s: %w", dir, err)
	}
	l.watcher = w

	// 解析节流：编辑器偶尔会一次保存触发多个事件
	const debounce = 100 * time.Millisecond
	var pending *time.Timer

	go func() {
		for {
			select {
			case <-l.closeCh:
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				// 只关心目标 yaml 文件，且事件是写/创建/重命名
				if filepath.Base(ev.Name) != filename {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
					continue
				}
				if pending != nil {
					pending.Stop()
				}
				pending = time.AfterFunc(debounce, func() {
					l.reloadAndNotify(onChange)
				})
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				// 错误通道一般是底层句柄问题；记录但不中断 watcher
				_ = err
			}
		}
	}()

	return nil
}

// reloadAndNotify 用最新文件内容重新解析并触发回调。
// 解析失败时保留旧值（避免坏配置把线上服务搞坏），只打 log。
func (l *Loader) reloadAndNotify(onChange func(*LLMConfig)) {
	// 重新读文件刷新 viper 内部缓存；失败则保留旧值
	if err := l.loader.ReadInConfig(); err != nil {
		return
	}
	cfg, err := unmarshalLLMConfig(l.loader)
	if err != nil {
		return
	}
	// 重新收集 yaml 显式 key 集合（同 Load 路径），保证热加载与首次加载行为一致
	yamlKeys := flattenViperKeys(l.loader.AllSettings())
	if err := resolveProviderDefaults(l.loader, yamlKeys, cfg); err != nil {
		// 解析后 provider 校验失败同样保留旧值，避免热加载把线上服务搞坏
		return
	}
	l.mu.Lock()
	l.cfg = cfg
	l.mu.Unlock()
	onChange(cfg)
}

// defaultTemplateYAML 是 WriteTemplate=true 时落盘的默认内容。
// 注释里把字段说清楚，方便用户编辑。
const defaultTemplateYAML = `# boring 配置文件
# 字段说明见 app/internal/config/config.go
# 优先级：命令行 flag > 环境变量 (EnvPrefix_*) > 本文件

# 程序内置的 LLM 厂商预设。选一个后，未显式配置的 baseUrl / sdk / model.id 会用 provider 的默认值填充；
# 显式 baseUrl 仍可覆盖（自建代理场景）；显式 sdk 必须落在该 provider 允许的协议列表内，否则 fail-fast。
provider: openai                # openai | anthropic | deepseek | 留空走老路径（手动指定 sdk + baseUrl）
#baseUrl: https://api.openai.com/v1  # 可选；留空走 provider / Sdk.DefaultBaseURL() 默认
apiKey: sk-replace-me          # LLM provider API key
#sdk: openai-chat               # openai-chat | openai-response | anthropic-message | deepseek
model:
#  name: GPT-4o                 # 仅展示用，不参与请求
  id: gpt-4o                   # 实际下发给 provider 的模型 ID；留空走 provider 默认
#  maxResponse: 1024            # 单次响应 token 上限；0 走 provider 兜底
#  maxContext: 128000           # 上下文窗口大小；0 表示不限制
`
