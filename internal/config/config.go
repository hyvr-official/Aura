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
		Port:      os.Getenv("APP_PORT"),
		DBPath:    os.Getenv("AURA_DB_PATH"),
		FilesRoot: os.Getenv("AURA_FILES_ROOT"),
	}

	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "aura.db"
	}
	if cfg.FilesRoot == "" {
		cfg.FilesRoot = "/"
	}

	return cfg, nil
}
