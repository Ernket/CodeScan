package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"codescan/internal/config"
	"codescan/internal/database"

	"github.com/google/uuid"
)

const (
	DataDir     = "data"
	ProjectsDir = "projects"
	ConfigFile  = "data/config.json"
)

type Config = config.Config

func main() {
	fmt.Println("Initializing CodeScan System...")

	// 1. Create Directories
	dirs := []string{DataDir, ProjectsDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			fmt.Printf("Error creating directory %s: %v\n", dir, err)
			return
		}
		fmt.Printf("Verified directory: %s\n", dir)
	}

	// 2. Load or Create Config
	var cfg Config
	if _, err := os.Stat(ConfigFile); err == nil {
		loaded, err := config.LoadConfigFile(ConfigFile)
		if err != nil {
			fmt.Printf("Error loading config file %s: %v\n", ConfigFile, err)
			return
		}
		cfg = loaded
	}

	// 3. Setup token signing key
	if cfg.AuthKey == "" {
		fmt.Println("Generating new token signing key...")
		cfg.AuthKey = strings.ReplaceAll(uuid.New().String(), "-", "")
	} else {
		fmt.Println("Existing token signing key found.")
	}

	// 4. Setup Database Config (Interactive or Defaults)
	if cfg.DBConfig.Host == "" {
		cfg.DBConfig.Host = "127.0.0.1"
	}
	if cfg.DBConfig.Port == 0 {
		cfg.DBConfig.Port = 3306
	}
	if cfg.DBConfig.User == "" {
		cfg.DBConfig.User = "root"
	}
	if cfg.DBConfig.Password == "" {
		cfg.DBConfig.Password = os.Getenv("CODESCAN_DB_PASSWORD")
	}
	if cfg.DBConfig.DBName == "" {
		cfg.DBConfig.DBName = "codescan"
	}
	cfg.ScannerConfig, _ = config.NormalizeScannerConfig(cfg.ScannerConfig)
	cfg.OrchestrationConfig = config.NormalizeOrchestrationConfig(cfg.OrchestrationConfig)

	// Save Config
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(ConfigFile, data, 0644); err != nil {
		fmt.Printf("Error saving config: %v\n", err)
		return
	}
	fmt.Println("Configuration saved.")

	// 5. Initialize Database
	fmt.Println("Initializing Database...")

	rootDB, err := database.OpenMySQL(&cfg.DBConfig, false)
	if err != nil {
		fmt.Printf("Error connecting to MySQL (root): %v\n", err)
		fmt.Println("Please check if MySQL is running and credentials are correct.")
		return
	}

	dbExisted, err := database.EnsureDatabase(rootDB, cfg.DBConfig.DBName)
	if err != nil {
		fmt.Printf("Error ensuring database: %v\n", err)
		return
	}
	if dbExisted {
		fmt.Printf("Database '%s' already existed.\n", cfg.DBConfig.DBName)
	} else {
		fmt.Printf("Database '%s' created.\n", cfg.DBConfig.DBName)
	}

	db, err := database.OpenMySQL(&cfg.DBConfig, true)
	if err != nil {
		fmt.Printf("Error connecting to database: %v\n", err)
		return
	}

	fmt.Println("Migrating database schema...")
	repairs, err := database.EnsureSchema(db)
	if err != nil {
		fmt.Printf("Error migrating database: %v\n", err)
		return
	}
	fmt.Println("Database schema migrated successfully.")
	if len(repairs) == 0 {
		fmt.Println("Schema check: no repairs were needed.")
	} else {
		fmt.Println("Schema check: repaired columns:")
		for _, repair := range repairs {
			fmt.Printf(" - %s\n", repair)
		}
	}

	adminResult, err := database.EnsureDefaultSuperAdmin(db)
	if err != nil {
		fmt.Printf("Error ensuring default super admin: %v\n", err)
		return
	}

	fmt.Println("==================================================")
	fmt.Printf("TOKEN SIGNING KEY: %s\n", cfg.AuthKey)
	fmt.Printf("DB Host: %s:%d\n", cfg.DBConfig.Host, cfg.DBConfig.Port)
	fmt.Printf("DB Name: %s\n", cfg.DBConfig.DBName)
	if adminResult.Created || adminResult.Updated {
		fmt.Printf("SUPER ADMIN USERNAME: %s\n", adminResult.Username)
		if adminResult.GeneratedPassword {
			fmt.Printf("GENERATED SUPER ADMIN PASSWORD: %s\n", adminResult.Password)
			fmt.Println("Store this password now; it will not be shown again.")
		} else {
			fmt.Println("SUPER ADMIN PASSWORD: loaded from CODESCAN_ADMIN_PASSWORD")
		}
	} else {
		fmt.Println("SUPER ADMIN: existing account found; password unchanged.")
	}
	fmt.Println("==================================================")
	fmt.Println("Initialization Complete.")
}
