package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port                int    `yaml:"port"`
	TickRateHz          int    `yaml:"tick_rate_hz"`
	MaxPlayers          int    `yaml:"max_players"`
	DBPath              string `yaml:"db_path"`
	LogLevel            string `yaml:"log_level"`
	EmissionIntervalMin int    `yaml:"emission_interval_min"`
	AdminPipe           string `yaml:"admin_pipe"`
}

func Load(path string) (*Config, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(file, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}
