package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	configPath := flag.String("config", "", "Path to TOML config file (default: look for config.toml in current dir)")
	printConfig := flag.Bool("print-config", false, "Print an example config file and exit")
	flag.Parse()

	if *printConfig {
		fmt.Print(ExampleConfig())
		os.Exit(0)
	}

	// Resolve config path: flag > env > default locations
	path := *configPath
	if path == "" {
		path = os.Getenv("CONFIG_FILE")
	}
	if path == "" {
		// Check default locations
		for _, candidate := range []string{"config.toml", "/config/config.toml", "/etc/self-improvement-mcp/config.toml"} {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	if path != "" {
		log.Printf("loaded config: %s", path)
	} else {
		log.Printf("using default config (no config file found)")
	}
	log.Printf("backend: %s", cfg.Backend.Type)

	backend, err := NewBackend(cfg)
	if err != nil {
		log.Fatalf("backend init failed: %v", err)
	}
	if backend == nil {
		log.Fatalf("unknown backend type: %q (must be 'sqlite' or 'chroma')", cfg.Backend.Type)
	}
	defer backend.Close()

	srv := NewServer(backend)
	mux := http.NewServeMux()
	srv.Routes(mux)

	addr := cfg.Server.Addr
	log.Printf("self-improvement-mcp listening on %s", addr)
	fmt.Printf("MCP endpoint:  http://localhost%s/mcp\n", addr)
	fmt.Printf("Health check:  http://localhost%s/health\n", addr)
	fmt.Printf("Backend:       %s\n", cfg.Backend.Type)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
