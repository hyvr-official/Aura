package config

type Config struct {
	Port      string
	DBPath    string
	FilesRoot string
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:      "8445",
		DBPath:    "aura.db",
		FilesRoot: "/",
	}

	return cfg, nil
}
