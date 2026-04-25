package database

import (
	"fmt"
	"strings"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"codescan/internal/config"
	"codescan/internal/model"
)

type schemaColumn struct {
	Table  string
	Column string
	Type   string
}

var requiredSchemaColumns = []schemaColumn{
	{Table: "tasks", Column: "result", Type: "LONGTEXT"},
	{Table: "task_stages", Column: "result", Type: "LONGTEXT"},
}

func BuildDSN(cfg *config.DBConfig, withDatabase bool) string {
	dbName := ""
	if withDatabase {
		dbName = cfg.DBName
	}
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		dbName,
	)
}

func OpenMySQL(cfg *config.DBConfig, withDatabase bool) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(BuildDSN(cfg, withDatabase)), &gorm.Config{})
	if err != nil {
		target := "mysql server"
		if withDatabase {
			target = fmt.Sprintf("database %q", cfg.DBName)
		}
		return nil, fmt.Errorf("error connecting to %s: %v", target, err)
	}
	return db, nil
}

func EnsureDatabase(rootDB *gorm.DB, dbName string) (bool, error) {
	var existingCount int64
	if err := rootDB.Raw(
		"SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?",
		dbName,
	).Scan(&existingCount).Error; err != nil {
		return false, fmt.Errorf("error checking database existence: %v", err)
	}

	if err := rootDB.Exec(
		fmt.Sprintf(
			"CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci",
			quoteIdentifier(dbName),
		),
	).Error; err != nil {
		return false, fmt.Errorf("error creating database: %v", err)
	}

	return existingCount > 0, nil
}

func EnsureSchema(db *gorm.DB) ([]string, error) {
	if err := db.AutoMigrate(
		&model.Task{},
		&model.TaskStage{},
		&model.TaskRun{},
		&model.TaskSubtask{},
		&model.TaskAgentRun{},
		&model.TaskEvent{},
		&model.TaskRoute{},
		&model.TaskFinding{},
	); err != nil {
		return nil, fmt.Errorf("error auto-migrating database: %v", err)
	}

	repairs := []string{}
	for _, column := range requiredSchemaColumns {
		repaired, err := ensureColumnType(db, column.Table, column.Column, column.Type)
		if err != nil {
			return repairs, err
		}
		if repaired {
			repairs = append(repairs, fmt.Sprintf("%s.%s -> %s", column.Table, column.Column, column.Type))
		}
	}

	return repairs, nil
}

func ensureColumnType(db *gorm.DB, tableName, columnName, expectedType string) (bool, error) {
	var columnDataType string
	if err := db.Raw(
		`SELECT DATA_TYPE
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?
		LIMIT 1`,
		tableName,
		columnName,
	).Scan(&columnDataType).Error; err != nil {
		return false, fmt.Errorf("error inspecting %s.%s: %v", tableName, columnName, err)
	}

	if columnDataType == "" {
		return false, fmt.Errorf("column %s.%s not found after migration", tableName, columnName)
	}
	if strings.EqualFold(columnDataType, expectedType) {
		return false, nil
	}

	if err := db.Exec(
		fmt.Sprintf(
			"ALTER TABLE %s MODIFY COLUMN %s %s",
			quoteIdentifier(tableName),
			quoteIdentifier(columnName),
			expectedType,
		),
	).Error; err != nil {
		return false, fmt.Errorf("error repairing %s.%s to %s: %v", tableName, columnName, expectedType, err)
	}

	return true, nil
}

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
