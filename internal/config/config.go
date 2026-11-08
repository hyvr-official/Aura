package config

import (
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port      string
	DBPath    string
	FilesRoot string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		Port:      "8445",
		DBPath:    os.Getenv("AURA_DB_PATH"),
		FilesRoot: os.Getenv("AURA_FILES_ROOT"),
	}

	if cfg.DBPath == "" {
		cfg.DBPath = "aura.db"
	}
	if cfg.FilesRoot == "" {
		cfg.FilesRoot = "/"
	}

	return cfg, nil
}
