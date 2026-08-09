package settings

import "fmt"

const (
	DefaultLogLevel         = "info"
	DefaultMaxNestedDepth   = 5
	DefaultMaxResponseNodes = 1024
	DefaultMaxResponseBytes = 1048576
	DefaultMaxCacheEntries  = 5000
	DefaultMaxFetchFanOut   = 8

	HardMaxNestedDepth   = 10
	HardMaxResponseNodes = 65536
	HardMaxResponseBytes = 16 << 20
	HardMaxCacheEntries  = 100000
	HardMaxFetchFanOut   = 64
)

type Config struct {
	DataDir          string `json:"data_dir,omitempty"`
	LogLevel         string `json:"log_level,omitempty"`
	MaxNestedDepth   int    `json:"max_nested_depth,omitempty"`
	MaxResponseNodes int    `json:"max_response_nodes,omitempty"`
	MaxResponseBytes int    `json:"max_response_bytes,omitempty"`
	MaxCacheEntries  int    `json:"max_cache_entries,omitempty"`
	MaxFetchFanOut   int    `json:"max_fetch_fan_out,omitempty"`
}

func Defaults() Config {
	return Config{
		LogLevel:         DefaultLogLevel,
		MaxNestedDepth:   DefaultMaxNestedDepth,
		MaxResponseNodes: DefaultMaxResponseNodes,
		MaxResponseBytes: DefaultMaxResponseBytes,
		MaxCacheEntries:  DefaultMaxCacheEntries,
		MaxFetchFanOut:   DefaultMaxFetchFanOut,
	}
}

func ApplyDefaults(c Config) Config {
	d := Defaults()
	if c.DataDir != "" {
		d.DataDir = c.DataDir
	}
	if c.LogLevel != "" {
		d.LogLevel = c.LogLevel
	}
	if c.MaxNestedDepth != 0 {
		d.MaxNestedDepth = c.MaxNestedDepth
	}
	if c.MaxResponseNodes != 0 {
		d.MaxResponseNodes = c.MaxResponseNodes
	}
	if c.MaxResponseBytes != 0 {
		d.MaxResponseBytes = c.MaxResponseBytes
	}
	if c.MaxCacheEntries != 0 {
		d.MaxCacheEntries = c.MaxCacheEntries
	}
	if c.MaxFetchFanOut != 0 {
		d.MaxFetchFanOut = c.MaxFetchFanOut
	}
	return d
}

func Validate(c Config) error {
	c = ApplyDefaults(c)
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return fmt.Errorf("settings.invalid_log_level: %s", c.LogLevel)
	}
	if c.MaxNestedDepth < 1 || c.MaxNestedDepth > HardMaxNestedDepth {
		return fmt.Errorf("settings.max_nested_depth_out_of_range: %d", c.MaxNestedDepth)
	}
	if c.MaxResponseNodes < 1 || c.MaxResponseNodes > HardMaxResponseNodes {
		return fmt.Errorf("settings.max_response_nodes_out_of_range: %d", c.MaxResponseNodes)
	}
	if c.MaxResponseBytes < 1 || c.MaxResponseBytes > HardMaxResponseBytes {
		return fmt.Errorf("settings.max_response_bytes_out_of_range: %d", c.MaxResponseBytes)
	}
	if c.MaxCacheEntries < 1 || c.MaxCacheEntries > HardMaxCacheEntries {
		return fmt.Errorf("settings.max_cache_entries_out_of_range: %d", c.MaxCacheEntries)
	}
	if c.MaxFetchFanOut < 1 || c.MaxFetchFanOut > HardMaxFetchFanOut {
		return fmt.Errorf("settings.max_fetch_fan_out_out_of_range: %d", c.MaxFetchFanOut)
	}
	return nil
}
