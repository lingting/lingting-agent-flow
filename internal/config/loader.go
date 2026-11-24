package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath 是未显式指定配置文件时使用的项目约定路径。
const DefaultPath = "configs/agentflow.yaml"

// Load 读取并校验配置文件，返回校验通过的配置。
//
// path 为空时使用 DefaultPath；文件不存在时返回明确错误，不回退到默认值。
func Load(path string) (*Config, error) {
	resolved := strings.TrimSpace(path)
	if resolved == "" {
		resolved = DefaultPath
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf(
				"config: 配置文件不存在: %s（使用 --config 指定路径，或复制 configs/example.yaml）: %w",
				resolved,
				err,
			)
		}
		return nil, fmt.Errorf("config: 读取配置文件失败: %s: %w", resolved, err)
	}
	return decode(resolved, data)
}

// LoadOrDefault 读取并校验配置文件；默认路径不存在时返回内置默认配置。
//
// 与 Load 的差别只有一种情况：path 为空且 DefaultPath 指向的文件不存在时，返回
// config.Default，使服务端可以在没有任何配置文件的情况下启动。显式指定了路径而
// 文件不存在时仍然报错，不会静默改用默认值。
func LoadOrDefault(path string) (*Config, error) {
	if strings.TrimSpace(path) != "" {
		return Load(path)
	}
	data, err := os.ReadFile(DefaultPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			cfg := Default()
			if validateErr := cfg.Validate(); validateErr != nil {
				return nil, validateErr
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("config: 读取配置文件失败: %s: %w", DefaultPath, err)
	}
	return decode(DefaultPath, data)
}

// decode 解析 YAML 内容并执行校验。
//
// 未知字段与重复字段都会被拒绝，避免拼写错误被静默忽略。
func decode(path string, data []byte) (*Config, error) {
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("config: 配置文件为空: %s", path)
		}
		return nil, fmt.Errorf("config: 解析配置文件失败: %s: %w", path, err)
	}
	var trailing yaml.Node
	switch err := decoder.Decode(&trailing); {
	case err == nil:
		return nil, fmt.Errorf("config: 配置文件只允许一个 YAML 文档: %s", path)
	case errors.Is(err, io.EOF):
	default:
		return nil, fmt.Errorf("config: 解析配置文件失败: %s: %w", path, err)
	}
	cfg.SourcePath = path
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
