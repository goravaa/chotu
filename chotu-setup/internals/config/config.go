package internals

import (
	"crypto/sha256"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Machine   MachineConfig   `yaml:"machine"`
	S3        S3Config        `yaml:"s3"`
	Inference InferenceConfig `yaml:"inference"`
	Coding    CodingConfig    `yaml:"coding"`
	Tooling   ToolingConfig   `yaml:"tooling"`
}

type MachineConfig struct {
	InstanceType string        `yaml:"instance_type"`
	ImageID      string        `yaml:"image_id"`
	Region       string        `yaml:"region"`
	SSHKeyPath   string        `yaml:"ssh_key_path"`
	UseSpot      bool          `yaml:"use_spot"`
	Storage      StorageConfig `yaml:"storage"`
}

type StorageConfig struct {
	RootDiskSizeGB int    `yaml:"root_disk_size_gb"`
	RootDiskType   string `yaml:"root_disk_type"`
	Persist        bool   `yaml:"persist"`
}

type S3Config struct {
	Enabled bool       `yaml:"enabled"`
	Bucket  string     `yaml:"bucket_name"`
	Region  string     `yaml:"region"`
	Sync    SyncConfig `yaml:"sync"`
}

type SyncConfig struct {
	Prefix     string   `yaml:"prefix"`
	Interval   string   `yaml:"interval"`
	Exclusions []string `yaml:"exclusions"`
}

type InferenceConfig struct {
	Runtime      string         `yaml:"runtime"`
	Model        string         `yaml:"model"`
	Quantization string         `yaml:"quantization"`
	Access       string         `yaml:"access"`
	Image        string         `yaml:"image"`
	Port         int            `yaml:"port"`
	Resources    ResourceConfig `yaml:"resources"`
}

type ResourceConfig struct {
	MaxModelLen          int     `yaml:"max_model_len"`
	GPUMemoryUtilization float64 `yaml:"gpu_memory_utilization"`
	MaxOutputTokens      int     `yaml:"max_output_tokens"`
}

type CodingConfig struct {
	OpenCode bool `yaml:"opencode"`
}

type ToolingConfig struct {
	Packages []string `yaml:"packages"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return &cfg, nil
}

func Hash(cfg *Config) (string, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config for hashing: %w", err)
	}

	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}
