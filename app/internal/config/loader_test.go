package config

import (
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

const sampleYAML = `
baseUrl: https://api.example.com/v1
apiKey: test-key
sdk: openai-response
model:
  name: GPT-4o
  id: gpt-4o
  maxResponse: 2048
  maxContext: 128000
`

func TestLoad_BasicYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(sampleYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	loader, err := Load(path, Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })

	cfg := loader.Config()
	if cfg.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "test-key")
	}
	if cfg.Sdk != SdkOpenAIResponse {
		t.Errorf("Sdk = %q, want %q", cfg.Sdk, SdkOpenAIResponse)
	}
	wantURL, _ := url.Parse("https://api.example.com/v1")
	if cfg.BaseURL.String() != wantURL.String() {
		t.Errorf("BaseURL = %q, want %q", cfg.BaseURL.String(), wantURL.String())
	}
	if cfg.BaseURL.String() != "https://api.example.com/v1" {
		t.Errorf("BaseURL = %q, want host+scheme preserved", cfg.BaseURL.String())
	}
	if cfg.Model.ID != "gpt-4o" {
		t.Errorf("Model.ID = %q, want %q", cfg.Model.ID, "gpt-4o")
	}
	if cfg.Model.MaxResponse != 2048 {
		t.Errorf("Model.MaxResponse = %d, want 2048", cfg.Model.MaxResponse)
	}
	if cfg.Model.MaxContext != 128000 {
		t.Errorf("Model.MaxContext = %d, want 128000", cfg.Model.MaxContext)
	}
}

func TestLoad_FileNotFound_ErrorsWithoutTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.yaml")

	_, err := Load(path, Options{})
	if err == nil {
		t.Fatal("Load 应返回错误，文件不存在且未启用 WriteTemplate")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("未启用 WriteTemplate 时不应创建文件，stat err = %v", statErr)
	}
}

func TestLoad_AutoWriteTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "config.yaml")

	loader, err := Load(path, Options{WriteTemplate: true})
	if err != nil {
		t.Fatalf("Load with WriteTemplate: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("模板未落盘: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("模板内容为空")
	}
	// 解析出来的值应来自模板默认（SdkOpenAIChat 是 SetDefault 的值）
	cfg := loader.Config()
	if cfg.Sdk != SdkOpenAIChat {
		t.Errorf("模板默认 Sdk = %q, want %q", cfg.Sdk, SdkOpenAIChat)
	}
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(sampleYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TESTCFG_APIKEY", "env-override")

	loader, err := Load(path, Options{EnvPrefix: "TESTCFG"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })

	if got := loader.Config().APIKey; got != "env-override" {
		t.Errorf("APIKey = %q, want env-override", got)
	}
}

func TestLoad_FlagOverridesEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(sampleYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TESTFLAG_APIKEY", "from-env")

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("api-key", "", "API key")
	if err := fs.Parse([]string{"--api-key=from-flag"}); err != nil {
		t.Fatal(err)
	}

	loader, err := Load(path, Options{EnvPrefix: "TESTFLAG", FlagSet: fs})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })

	if got := loader.Config().APIKey; got != "from-flag" {
		t.Errorf("APIKey = %q, want from-flag (flag > env)", got)
	}
}

func TestLoad_Watch_TriggersOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(sampleYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	var gotKey atomic.Value
	gotKey.Store("")

	loader, err := Load(path, Options{
		Watch: true,
		OnConfigChange: func(c *LLMConfig) {
			calls.Add(1)
			gotKey.Store(c.APIKey)
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = loader.Close() })

	// 修改文件，触发 watcher（debounce 100ms + 触发延迟）
	updated := []byte(`
baseUrl: https://api.example.com/v1
apiKey: reloaded-key
sdk: openai-chat
model:
  id: gpt-4o
  maxResponse: 0
  maxContext: 0
`)
	// 给 watcher 一点时间建立监听
	time.Sleep(50 * time.Millisecond)
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		t.Fatal(err)
	}

	// 等回调触发（debounce 100ms + 解析 + 调度）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if calls.Load() == 0 {
		t.Fatal("OnConfigChange 未被触发")
	}
	if got := gotKey.Load().(string); got != "reloaded-key" {
		t.Errorf("回调内 APIKey = %q, want %q", got, "reloaded-key")
	}
}

func TestLoad_Watch_RequiresOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(sampleYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path, Options{Watch: true})
	if err == nil {
		t.Fatal("Watch=true 但没传 OnConfigChange，应报错")
	}
}
