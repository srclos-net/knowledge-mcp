package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server   ServerConfig   `toml:"server"`
	Backend  BackendConfig  `toml:"backend"`
	SQLite   SQLiteConfig   `toml:"sqlite"`
	Chroma   ChromaConfig   `toml:"chroma"`
	Supabase SupabaseConfig `toml:"supabase"`
}

type ServerConfig struct {
	Addr string `toml:"addr"`
}

type BackendConfig struct {
	Type string `toml:"type"` // "sqlite" or "chroma"
}

type SQLiteConfig struct {
	Path string `toml:"path"`
}

type ChromaConfig struct {
	URL            string `toml:"url"`
	Tenant         string `toml:"tenant"`          // default: "default_tenant"
	Database       string `toml:"database"`        // default: "default_database"
	Collection     string `toml:"collection"`
	EmbeddingModel string `toml:"embedding_model"` // ollama model name, or "" to use chroma's default
	OllamaURL      string `toml:"ollama_url"`
}

type SupabaseConfig struct {
	URL            string `toml:"url"`             // postgres connection string
	Table          string `toml:"table"`           // default: "learnings"
	EmbeddingModel string `toml:"embedding_model"` // ollama model name, or "" for ILIKE text fallback
	OllamaURL      string `toml:"ollama_url"`
	EmbeddingDim   int    `toml:"embedding_dim"` // default: 768 (nomic-embed-text)
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Addr: ":8080",
		},
		Backend: BackendConfig{
			Type: "sqlite",
		},
		SQLite: SQLiteConfig{
			Path: "/data/learnings.db",
		},
		Chroma: ChromaConfig{
			URL:            "http://chroma:8000",
			Tenant:         "default_tenant",
			Database:       "default_database",
			Collection:     "self_improvement",
			EmbeddingModel: "",
			OllamaURL:      "http://ollama:11434",
		},
		Supabase: SupabaseConfig{
			Table:        "learnings",
			OllamaURL:    "http://ollama:11434",
			EmbeddingDim: 768,
		},
	}
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	// Apply defaults for any chroma fields left empty
	if cfg.Chroma.Tenant == "" {
		cfg.Chroma.Tenant = "default_tenant"
	}
	if cfg.Chroma.Database == "" {
		cfg.Chroma.Database = "default_database"
	}

	return cfg, nil
}

func ExampleConfig() string {
	return `# self-improvement-mcp configuration

[server]
addr = ":8080"

[backend]
# "sqlite" or "chroma"
type = "sqlite"

[sqlite]
path = "/data/learnings.db"

[chroma]
url        = "http://chroma:8000"
tenant     = "default_tenant"
database   = "default_database"
collection = "self_improvement"
# Optional: use ollama for real semantic embeddings
# embedding_model = "nomic-embed-text"
# ollama_url      = "http://ollama:11434"

[supabase]
# url = "postgres://postgres.xxxx:password@aws-0-us-east-1.pooler.supabase.com:6543/postgres"
# table = "learnings"
# Optional: use ollama for pgvector semantic search; falls back to ILIKE otherwise
# embedding_model = "nomic-embed-text"
# ollama_url      = "http://ollama:11434"
# embedding_dim   = 768
`
}
