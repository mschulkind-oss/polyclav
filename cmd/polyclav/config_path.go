package main

import "path/filepath"

func defaultConfigPath(configDir string) string {
	return filepath.Join(configDir, "polyclav", "config.toml")
}
