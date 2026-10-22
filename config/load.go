package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Spider *SpiderConfig `yaml:"spider"`
	Model  *ModelConfig  `yaml:"model"`
}

// Load 读取 YAML 配置文件，返回 rod 配置与登录账号
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// API Key 以环境变量为最高优先级，避免密钥硬编码进代码/配置仓库。
	// 部署时注入 AI_SPIDER_API_KEY，yaml 中的 api_key 仅作本地兜底。
	if k := os.Getenv("AI_SPIDER_API_KEY"); k != "" {
		cfg.Model.APIKey = k
	}

	return &cfg, nil
}
