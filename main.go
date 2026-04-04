package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"

	"codescan/internal/api/router"
	"codescan/internal/config"
	"codescan/internal/database"
)

func main() {
	// Note: Initialization of directories and auth key is now handled by cmd/init/main.go

	// Define config flag
	configFile := flag.String("config", "data/config.json", "Path to configuration file")
	flag.Parse()

	// Load Config
	var cfg config.Config
	if _, err := os.Stat(*configFile); err == nil {
		data, err := os.ReadFile(*configFile)
		if err == nil {
			if err := json.Unmarshal(data, &cfg); err != nil {
				fmt.Printf("Warning: Failed to parse config file: %v\n", err)
			}
		}
	} else if *configFile != "data/config.json" {
		fmt.Printf("Warning: Config file %s not found\n", *configFile)
	}

	// Override with Environment Variables
	if v := os.Getenv("CODESCAN_AUTH_KEY"); v != "" {
		cfg.AuthKey = v
	}
	if v := os.Getenv("CODESCAN_DB_HOST"); v != "" {
		cfg.DBConfig.Host = v
	}
	if v := os.Getenv("CODESCAN_DB_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.DBConfig.Port = port
		}
	}
	if v := os.Getenv("CODESCAN_DB_USER"); v != "" {
		cfg.DBConfig.User = v
	}
	if v := os.Getenv("CODESCAN_DB_PASSWORD"); v != "" {
		cfg.DBConfig.Password = v
	}
	if v := os.Getenv("CODESCAN_DB_NAME"); v != "" {
		cfg.DBConfig.DBName = v
	}
	if v := os.Getenv("CODESCAN_AI_API_KEY"); v != "" {
		cfg.AIConfig.APIKey = v
	}
	if v := os.Getenv("CODESCAN_AI_BASE_URL"); v != "" {
		cfg.AIConfig.BaseURL = v
	}
	if v := os.Getenv("CODESCAN_AI_MODEL"); v != "" {
		cfg.AIConfig.Model = v
	}

	// Set defaults for AI config
	if cfg.AIConfig.Model == "" {
		cfg.AIConfig.Model = "gemini-3-pro-high"
	}
	var scannerWarnings []string
	cfg.ScannerConfig, scannerWarnings = config.NormalizeScannerConfig(cfg.ScannerConfig)
	for _, warning := range scannerWarnings {
		fmt.Printf("Warning: %s\n", warning)
	}

	// Expose AI config globally for scanner
	config.AI = cfg.AIConfig
	config.Scanner = cfg.ScannerConfig

	if cfg.AuthKey == "" {
		fmt.Println("Error: Auth Key not found. Please run 'go run cmd/init/main.go' first.")
		return
	}

	// Connect DB
	if err := database.InitDB(&cfg.DBConfig); err != nil {
		fmt.Printf("Error connecting to database: %v\n", err)
		return
	}

	fmt.Println("==================================================")
	fmt.Println("Loaded AUTH KEY from config/environment")
	fmt.Println("Connected to Database")
	fmt.Println("==================================================")

	// Setup Gin
	r := gin.Default()

	// Init Router
	router.InitRouter(r, cfg.AuthKey)

	r.Run(":8089")
}
