package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const DefaultConfigPath = "config.json"
const DefaultAccessTokenPath = "access_token.json"

type Config struct {
	Mode   string       `json:"mode"`
	Signup SignupConfig `json:"signup"`
	Server ServerConfig `json:"server"`
}

type SignupConfig struct {
	APIKey string `json:"api_key"`
	Domain string `json:"domain"`
}

type ServerConfig struct {
	AccessToken   string   `json:"access_token"`
	Listen        string   `json:"listen"`
	OpenAIAPIKey  string   `json:"openai_api_key"`
	BaseModelName string   `json:"base_model_name"`
	EnabledTools  []string `json:"enabled_tools"`
}

type AccessTokenFile struct {
	AccessToken string `json:"access_token"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	VerifyLink  string `json:"verify_link"`
	CreatedAt   string `json:"created_at"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) ResolvedMode(flagMode string) string {
	if flagMode != "" {
		return flagMode
	}
	if c.Mode != "" {
		return c.Mode
	}
	return "signup"
}

func (c ServerConfig) ResolvedListen() string {
	if c.Listen != "" {
		return c.Listen
	}
	return "127.0.0.1:8787"
}

func (c ServerConfig) ResolvedModel() string {
	if c.BaseModelName != "" {
		return c.BaseModelName
	}
	return "trinity-large-thinking"
}

func (c ServerConfig) SupportedModels() []string {
	return []string{
		"trinity-mini",
		"trinity-large-preview",
		c.ResolvedModel(),
	}
}

func (c ServerConfig) ResolvedAccessToken() (string, error) {
	if token := strings.TrimSpace(c.AccessToken); token != "" {
		return token, nil
	}

	saved, err := LoadAccessTokenFile(DefaultAccessTokenPath)
	if err != nil {
		return "", fmt.Errorf("config.server.access_token is required or %s must exist", DefaultAccessTokenPath)
	}
	return saved.AccessToken, nil
}

func SaveAccessTokenFile(path, accessToken, email, password, verifyLink string) error {
	payload := AccessTokenFile{
		AccessToken: accessToken,
		Email:       email,
		Password:    password,
		VerifyLink:  verifyLink,
		CreatedAt:   time.Now().Format(time.RFC3339),
	}

	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func LoadAccessTokenFile(path string) (*AccessTokenFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var saved AccessTokenFile
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, err
	}
	if strings.TrimSpace(saved.AccessToken) == "" {
		return nil, fmt.Errorf("%s missing access_token", path)
	}
	return &saved, nil
}

const DefaultTokensDir = "tokens"

// LoadAllTokensFromDir 扫描指定目录，返回所有有效 access_token 列表。
// 文件必须是合法的 AccessTokenFile JSON 且 access_token 非空。
// 若目录不存在或为空，返回空切片和 nil error。
func LoadAllTokensFromDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tokens dir %s: %w", dir, err)
	}

	var tokens []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path := dir + "/" + name
		tf, err := LoadAccessTokenFile(path)
		if err != nil {
			continue // 跳过损坏文件
		}
		tokens = append(tokens, tf.AccessToken)
	}
	return tokens, nil
}
