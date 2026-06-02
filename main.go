package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/gin-gonic/gin"

	"codescan/internal/api/router"
	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/service/orchestration"
)

func main() {
	// Note: Initialization of directories and token signing key is handled by cmd/init/main.go.

	// Define config flag
	configFile := flag.String("config", "data/config.json", "Path to configuration file")
	flag.Parse()

	// Load Config
	var cfg config.Config
	if _, err := os.Stat(*configFile); err == nil {
		loaded, err := config.LoadConfigFile(*configFile)
		if err != nil {
			fmt.Printf("Error loading config file %s: %v\n", *configFile, err)
			return
		}
		cfg = loaded
	} else if *configFile != "data/config.json" {
		fmt.Printf("Warning: Config file %s not found\n", *configFile)
	}

	// Override with Environment Variables
	cfg = config.ApplyEnvOverrides(cfg)

	// Set defaults for AI config
	var aiWarnings []string
	cfg.AIConfig, aiWarnings = config.NormalizeAIConfig(cfg.AIConfig)
	for _, warning := range aiWarnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	var scannerWarnings []string
	cfg.ScannerConfig, scannerWarnings = config.NormalizeScannerConfig(cfg.ScannerConfig)
	for _, warning := range scannerWarnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	cfg.OrchestrationConfig = config.NormalizeOrchestrationConfig(cfg.OrchestrationConfig)

	// Expose AI config globally for scanner
	config.AI = cfg.AIConfig
	config.Scanner = cfg.ScannerConfig
	config.Orchestration = cfg.OrchestrationConfig

	if cfg.AuthKey == "" {
		fmt.Println("Error: token signing key not found. Please run 'go run cmd/init/main.go' first.")
		return
	}

	// Connect DB
	if err := database.InitDB(&cfg.DBConfig); err != nil {
		fmt.Printf("Error connecting to database: %v\n", err)
		return
	}
	if err := orchestration.DefaultManager().RecoverActiveRuns(); err != nil {
		fmt.Printf("Warning: failed to recover orchestration runs: %v\n", err)
	}

	fmt.Println("==================================================")
	fmt.Println("Loaded token signing key")
	fmt.Println("Connected to Database")
	fmt.Println("==================================================")

	// Setup Gin
	r := gin.Default()

	// Init Router
	router.InitRouterWithFrontend(r, cfg.AuthKey, frontendFS())

	r.Run(":8089")
}
