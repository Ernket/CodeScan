package model

import "time"

const (
	RoleSuperAdmin = "super_admin"
	RoleUser       = "user"
	RoleAdmin      = "admin"
	RoleObserver   = "observer"

	OrganizationRoleMember = "member"
	OrganizationRoleAdmin  = "admin"
)

type User struct {
	ID                      uint                     `json:"id" gorm:"primaryKey"`
	Username                string                   `json:"username" gorm:"type:varchar(64);uniqueIndex;not null"`
	PasswordHash            string                   `json:"-" gorm:"type:varchar(255);not null"`
	Role                    string                   `json:"role" gorm:"type:varchar(32);not null;index"`
	Enabled                 bool                     `json:"enabled" gorm:"not null;default:true"`
	TokenVersion            int                      `json:"-" gorm:"not null;default:1"`
	OrganizationMemberships []OrganizationMembership `json:"-" gorm:"foreignKey:UserID"`
	CreatedAt               time.Time                `json:"created_at"`
	UpdatedAt               time.Time                `json:"updated_at"`
}

type Organization struct {
	ID        uint           `json:"id" gorm:"primaryKey"`
	Name      string         `json:"name" gorm:"type:varchar(128);not null"`
	ParentID  *uint          `json:"parent_id,omitempty" gorm:"index"`
	Parent    *Organization  `json:"-" gorm:"foreignKey:ParentID"`
	Children  []Organization `json:"children,omitempty" gorm:"foreignKey:ParentID"`
	Path      string         `json:"path" gorm:"type:varchar(768);not null;index:idx_organizations_path,length:191"`
	Depth     int            `json:"depth" gorm:"not null;default:0;index"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type OrganizationMembership struct {
	ID             uint         `json:"id" gorm:"primaryKey"`
	UserID         uint         `json:"user_id" gorm:"not null;uniqueIndex:idx_org_membership_user_org;index"`
	OrganizationID uint         `json:"organization_id" gorm:"not null;uniqueIndex:idx_org_membership_user_org;index"`
	Role           string       `json:"role" gorm:"type:varchar(16);not null;index"`
	User           User         `json:"-" gorm:"foreignKey:UserID"`
	Organization   Organization `json:"organization" gorm:"foreignKey:OrganizationID"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}
