package config

import (
	"crypto/sha256"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Machine    MachineConfig    `yaml:"machine"`
	S3         S3Config         `yaml:"s3"`
	Inference  InferenceConfig  `yaml:"inference"`
	Coding     CodingConfig     `yaml:"coding"`
	Tooling    ToolingConfig    `yaml:"tooling"`
	StatusPage StatusPageConfig `yaml:"status_page"`
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
	// Persist selects the storage topology.
	//
	// false is the disposable spot machine: the EC2 instance store is mounted
	// at the data directory and the workspace, Docker root, model cache and
	// Chotu state all live on it, because the machine can be reclaimed at any
	// moment and nothing survives an interruption.
	//
	// true is the on-demand machine: the user sized a root disk that survives
	// the instance, so the workspace, Docker root, model cache and Chotu state
	// live on the root disk. The instance store is still mounted at the data
	// directory, but only for scratch space that is meant to be thrown away.
	Persist bool `yaml:"persist"`
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

// StatusPageConfig describes the progress page the user opens in a browser to
// see what the machine is doing. It runs as a container pulled from a registry,
// not as a process the setup daemon manages, so only the image and the port are
// configuration.
type StatusPageConfig struct {
	Enabled bool   `yaml:"enabled"`
	Image   string `yaml:"image"`
	Port    int    `yaml:"port"`
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

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate rejects combinations that would build a machine that cannot work.
//
// The important one is spot plus persist. In Chotu's storage model persistence
// means the state lives on a root disk that outlives the instance, and a spot
// instance by definition does not. Accepting it would put the workspace and the
// Docker root on a disk that disappears with the machine, which is the exact
// loss the user asked spot to avoid.
func (c Config) Validate() error {
	if c.Machine.UseSpot && c.Machine.Storage.Persist {
		return fmt.Errorf(
			"machine.use_spot is true and machine.storage.persist is true, which cannot " +
				"work: persistence keeps the workspace and the docker root on a root disk " +
				"that outlives the instance, and a spot instance does not. Either set " +
				"machine.use_spot to false for an on-demand machine with a root disk, or " +
				"set machine.storage.persist to false to keep everything on the instance store",
		)
	}

	if c.S3.Enabled && c.S3.Bucket == "" {
		return fmt.Errorf("s3.enabled is true but s3.bucket_name is empty")
	}

	if c.StatusPage.Enabled && c.StatusPage.Image == "" {
		return fmt.Errorf("status_page.enabled is true but status_page.image is empty")
	}

	return nil
}

func Hash(cfg *Config) (string, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config for hashing: %w", err)
	}

	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}
