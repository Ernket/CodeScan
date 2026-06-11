package database

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"

	"gorm.io/gorm"

	"codescan/internal/model"
	authsvc "codescan/internal/service/auth"
)

const defaultAdminUsername = "admin"

type DefaultSuperAdminResult struct {
	Created           bool
	Updated           bool
	Username          string
	Password          string
	GeneratedPassword bool
}

func EnsureDefaultSuperAdmin(db *gorm.DB) (DefaultSuperAdminResult, error) {
	result := DefaultSuperAdminResult{Username: defaultAdminUsername}

	var count int64
	if err := db.Model(&model.User{}).Where("role = ?", model.RoleSuperAdmin).Count(&count).Error; err != nil {
		return result, err
	}
	if count > 0 {
		return result, nil
	}

	password := strings.TrimSpace(os.Getenv("CODESCAN_ADMIN_PASSWORD"))
	if password == "" {
		generated, err := generateBootstrapPassword()
		if err != nil {
			return result, err
		}
		password = generated
		result.GeneratedPassword = true
	}
	result.Password = password

	hash, err := authsvc.HashPassword(password)
	if err != nil {
		return result, err
	}

	var user model.User
	query := db.Where("username = ?", defaultAdminUsername).Limit(1).Find(&user)
	if query.Error != nil {
		return result, query.Error
	}
	if query.RowsAffected == 0 {
		user = model.User{
			Username:     defaultAdminUsername,
			PasswordHash: hash,
			Role:         model.RoleSuperAdmin,
			Enabled:      true,
			TokenVersion: 1,
		}
		if err := db.Create(&user).Error; err != nil {
			return result, err
		}
		result.Created = true
	} else {
		updates := map[string]any{
			"password_hash": hash,
			"role":          model.RoleSuperAdmin,
			"enabled":       true,
			"token_version": gorm.Expr("token_version + ?", 1),
		}
		if user.TokenVersion <= 0 {
			updates["token_version"] = 1
		}
		if err := db.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
			return result, err
		}
		result.Updated = true
	}

	return result, nil
}

func generateBootstrapPassword() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
