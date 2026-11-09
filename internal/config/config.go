package config

type Config struct {
	Port        string
	WebhookPort string
	DBPath      string
	FilesRoot   string
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:        "8445",
		WebhookPort: "8600",
		DBPath:      "aura.db",
		FilesRoot:   "/",
	}

	return cfg, nil
}
