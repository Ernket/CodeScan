package organization

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"codescan/internal/model"
)

var (
	ErrInvalidName              = errors.New("organization name is required")
	ErrInvalidRole              = errors.New("organization role must be member or admin")
	ErrInvalidMove              = errors.New("organization cannot be moved under itself or a descendant")
	ErrOrganizationHasChildren  = errors.New("organization has child organizations")
	ErrOrganizationHasTasks     = errors.New("organization has projects")
	ErrOrganizationHasMembers   = errors.New("organization has members")
	ErrOrganizationInaccessible = errors.New("organization is not accessible")
)

type Assignment struct {
	OrganizationID uint   `json:"organization_id"`
	Role           string `json:"role"`
}

type AccessibleOrganization struct {
	ID            uint                     `json:"id"`
	Name          string                   `json:"name"`
	ParentID      *uint                    `json:"parent_id,omitempty"`
	Path          string                   `json:"path"`
	Depth         int                      `json:"depth"`
	EffectiveRole string                   `json:"effective_role,omitempty"`
	CreatedAt     time.Time                `json:"created_at"`
	UpdatedAt     time.Time                `json:"updated_at"`
	Children      []AccessibleOrganization `json:"children,omitempty"`
}

type treeNode struct {
	value    AccessibleOrganization
	children []*treeNode
}

func NormalizeMembershipRole(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case model.OrganizationRoleMember:
		return model.OrganizationRoleMember, nil
	case model.OrganizationRoleAdmin:
		return model.OrganizationRoleAdmin, nil
	default:
		return "", ErrInvalidRole
	}
}

func IsSuperAdmin(user model.User) bool {
	return user.Role == model.RoleSuperAdmin
}

func Create(db *gorm.DB, name string, parentID *uint) (model.Organization, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Organization{}, ErrInvalidName
	}

	var created model.Organization
	err := db.Transaction(func(tx *gorm.DB) error {
		depth := 0
		parentPath := ""
		if parentID != nil {
			var parent model.Organization
			if err := tx.First(&parent, "id = ?", *parentID).Error; err != nil {
				return err
			}
			depth = parent.Depth + 1
			parentPath = parent.Path
		}

		created = model.Organization{
			Name:     name,
			ParentID: parentID,
			Path:     "",
			Depth:    depth,
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		created.Path = childPath(parentPath, created.ID)
		if err := tx.Model(&model.Organization{}).
			Where("id = ?", created.ID).
			Updates(map[string]any{"path": created.Path, "depth": created.Depth}).Error; err != nil {
			return err
		}
		return tx.First(&created, "id = ?", created.ID).Error
	})
	return created, err
}

func Update(db *gorm.DB, id uint, name *string, parentID *uint, parentChanged bool) (model.Organization, error) {
	var updated model.Organization
	err := db.Transaction(func(tx *gorm.DB) error {
		var org model.Organization
		if err := tx.First(&org, "id = ?", id).Error; err != nil {
			return err
		}

		if name != nil {
			nextName := strings.TrimSpace(*name)
			if nextName == "" {
				return ErrInvalidName
			}
			org.Name = nextName
		}

		if parentChanged {
			if parentID != nil && *parentID == org.ID {
				return ErrInvalidMove
			}

			newDepth := 0
			parentPath := ""
			if parentID != nil {
				var parent model.Organization
				if err := tx.First(&parent, "id = ?", *parentID).Error; err != nil {
					return err
				}
				if org.Path != "" && strings.HasPrefix(parent.Path, org.Path) {
					return ErrInvalidMove
				}
				newDepth = parent.Depth + 1
				parentPath = parent.Path
			}

			oldPath := org.Path
			newPath := childPath(parentPath, org.ID)
			depthDelta := newDepth - org.Depth
			if err := moveSubtree(tx, oldPath, newPath, depthDelta); err != nil {
				return err
			}
			org.ParentID = parentID
			org.Path = newPath
			org.Depth = newDepth
		}

		if err := tx.Save(&org).Error; err != nil {
			return err
		}
		return tx.First(&updated, "id = ?", org.ID).Error
	})
	return updated, err
}

func Delete(db *gorm.DB, id uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var org model.Organization
		if err := tx.First(&org, "id = ?", id).Error; err != nil {
			return err
		}

		var count int64
		if err := tx.Model(&model.Organization{}).Where("parent_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrOrganizationHasChildren
		}
		if err := tx.Model(&model.Task{}).Where("organization_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrOrganizationHasTasks
		}
		if err := tx.Model(&model.OrganizationMembership{}).Where("organization_id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrOrganizationHasMembers
		}
		return tx.Delete(&org).Error
	})
}

func AllTree(db *gorm.DB) ([]AccessibleOrganization, error) {
	var orgs []model.Organization
	if err := db.Order("depth asc").Order("name asc").Find(&orgs).Error; err != nil {
		return nil, err
	}
	return buildTree(orgs, map[uint]string{}, true), nil
}

func AccessibleTree(db *gorm.DB, user model.User) ([]AccessibleOrganization, error) {
	orgs, roles, err := effectiveOrganizationRoles(db, user)
	if err != nil {
		return nil, err
	}
	return buildTree(orgs, roles, IsSuperAdmin(user)), nil
}

func ReadableOrganizationIDs(db *gorm.DB, user model.User) ([]uint, error) {
	if IsSuperAdmin(user) {
		return nil, nil
	}
	_, roles, err := effectiveOrganizationRoles(db, user)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(roles))
	for id := range roles {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func ReadableOrganizationSubtreeIDs(db *gorm.DB, user model.User, organizationID uint) ([]uint, error) {
	orgs, roles, err := effectiveOrganizationRoles(db, user)
	if err != nil {
		return nil, err
	}

	var selected *model.Organization
	for i := range orgs {
		if orgs[i].ID == organizationID {
			selected = &orgs[i]
			break
		}
	}
	if selected == nil {
		return nil, gorm.ErrRecordNotFound
	}
	if !IsSuperAdmin(user) && roles[organizationID] == "" {
		return nil, ErrOrganizationInaccessible
	}

	ids := make([]uint, 0)
	for _, org := range orgs {
		if selected.Path == "" || org.Path == "" || !strings.HasPrefix(org.Path, selected.Path) {
			continue
		}
		if IsSuperAdmin(user) || roles[org.ID] != "" {
			ids = append(ids, org.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func WritableOrganizationIDs(db *gorm.DB, user model.User) ([]uint, error) {
	if IsSuperAdmin(user) {
		return nil, nil
	}
	_, roles, err := effectiveOrganizationRoles(db, user)
	if err != nil {
		return nil, err
	}
	ids := make([]uint, 0)
	for id, role := range roles {
		if role == model.OrganizationRoleAdmin {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func CanWriteOrganization(db *gorm.DB, user model.User, organizationID uint) (bool, error) {
	if IsSuperAdmin(user) {
		var count int64
		if err := db.Model(&model.Organization{}).Where("id = ?", organizationID).Count(&count).Error; err != nil {
			return false, err
		}
		return count > 0, nil
	}
	role, err := EffectiveRoleForOrganization(db, user, organizationID)
	if err != nil {
		return false, err
	}
	return role == model.OrganizationRoleAdmin, nil
}

func EffectiveRoleForOrganization(db *gorm.DB, user model.User, organizationID uint) (string, error) {
	if IsSuperAdmin(user) {
		var count int64
		if err := db.Model(&model.Organization{}).Where("id = ?", organizationID).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return "", gorm.ErrRecordNotFound
		}
		return model.OrganizationRoleAdmin, nil
	}
	_, roles, err := effectiveOrganizationRoles(db, user)
	if err != nil {
		return "", err
	}
	return roles[organizationID], nil
}

func TaskPermissions(db *gorm.DB, user model.User, task model.Task) (model.TaskPermissions, error) {
	if IsSuperAdmin(user) {
		return model.TaskPermissions{CanRead: true, CanWrite: true}, nil
	}
	if task.OrganizationID == nil || *task.OrganizationID == 0 {
		return model.TaskPermissions{}, nil
	}
	role, err := EffectiveRoleForOrganization(db, user, *task.OrganizationID)
	if err != nil {
		return model.TaskPermissions{}, err
	}
	return model.TaskPermissions{
		CanRead:  role != "",
		CanWrite: role == model.OrganizationRoleAdmin,
	}, nil
}

func AttachTaskPermissions(db *gorm.DB, user model.User, tasks []model.Task) error {
	if IsSuperAdmin(user) {
		for i := range tasks {
			tasks[i].Permissions = model.TaskPermissions{CanRead: true, CanWrite: true}
		}
		return nil
	}
	_, roles, err := effectiveOrganizationRoles(db, user)
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].OrganizationID == nil {
			continue
		}
		role := roles[*tasks[i].OrganizationID]
		tasks[i].Permissions = model.TaskPermissions{
			CanRead:  role != "",
			CanWrite: role == model.OrganizationRoleAdmin,
		}
	}
	return nil
}

func ReplaceUserMemberships(db *gorm.DB, userID uint, assignments []Assignment) error {
	normalized, err := normalizeAssignments(assignments)
	if err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var userCount int64
		if err := tx.Model(&model.User{}).Where("id = ?", userID).Count(&userCount).Error; err != nil {
			return err
		}
		if userCount == 0 {
			return gorm.ErrRecordNotFound
		}
		if err := ensureOrganizationsExist(tx, normalized); err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&model.OrganizationMembership{}).Error; err != nil {
			return err
		}
		for _, assignment := range normalized {
			membership := model.OrganizationMembership{
				UserID:         userID,
				OrganizationID: assignment.OrganizationID,
				Role:           assignment.Role,
			}
			if err := tx.Create(&membership).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func normalizeAssignments(assignments []Assignment) ([]Assignment, error) {
	byOrg := make(map[uint]string)
	for _, assignment := range assignments {
		if assignment.OrganizationID == 0 {
			return nil, gorm.ErrRecordNotFound
		}
		role, err := NormalizeMembershipRole(assignment.Role)
		if err != nil {
			return nil, err
		}
		current := byOrg[assignment.OrganizationID]
		if current == model.OrganizationRoleAdmin || role == model.OrganizationRoleAdmin {
			byOrg[assignment.OrganizationID] = model.OrganizationRoleAdmin
		} else {
			byOrg[assignment.OrganizationID] = model.OrganizationRoleMember
		}
	}

	normalized := make([]Assignment, 0, len(byOrg))
	for orgID, role := range byOrg {
		normalized = append(normalized, Assignment{OrganizationID: orgID, Role: role})
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].OrganizationID < normalized[j].OrganizationID
	})
	return normalized, nil
}

func ensureOrganizationsExist(db *gorm.DB, assignments []Assignment) error {
	if len(assignments) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(assignments))
	for _, assignment := range assignments {
		ids = append(ids, assignment.OrganizationID)
	}
	var count int64
	if err := db.Model(&model.Organization{}).Where("id IN ?", ids).Count(&count).Error; err != nil {
		return err
	}
	if int(count) != len(ids) {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func effectiveOrganizationRoles(db *gorm.DB, user model.User) ([]model.Organization, map[uint]string, error) {
	var orgs []model.Organization
	if err := db.Order("depth asc").Order("name asc").Find(&orgs).Error; err != nil {
		return nil, nil, err
	}

	roles := make(map[uint]string)
	if IsSuperAdmin(user) {
		for _, org := range orgs {
			roles[org.ID] = model.OrganizationRoleAdmin
		}
		return orgs, roles, nil
	}

	var memberships []model.OrganizationMembership
	if err := db.Preload("Organization").Where("user_id = ?", user.ID).Find(&memberships).Error; err != nil {
		return nil, nil, err
	}
	for _, org := range orgs {
		for _, membership := range memberships {
			if membership.Organization.ID == 0 || membership.Organization.Path == "" || org.Path == "" {
				continue
			}
			if !strings.HasPrefix(org.Path, membership.Organization.Path) {
				continue
			}
			role, err := NormalizeMembershipRole(membership.Role)
			if err != nil {
				return nil, nil, err
			}
			if roles[org.ID] != model.OrganizationRoleAdmin {
				roles[org.ID] = role
			}
		}
	}
	return orgs, roles, nil
}

func buildTree(orgs []model.Organization, roles map[uint]string, includeAll bool) []AccessibleOrganization {
	nodes := make(map[uint]*treeNode)
	for _, org := range orgs {
		role := roles[org.ID]
		if !includeAll && role == "" {
			continue
		}
		nodes[org.ID] = &treeNode{value: AccessibleOrganization{
			ID:            org.ID,
			Name:          org.Name,
			ParentID:      org.ParentID,
			Path:          org.Path,
			Depth:         org.Depth,
			EffectiveRole: role,
			CreatedAt:     org.CreatedAt,
			UpdatedAt:     org.UpdatedAt,
			Children:      []AccessibleOrganization{},
		}}
	}

	roots := make([]*treeNode, 0)
	for _, org := range orgs {
		node := nodes[org.ID]
		if node == nil {
			continue
		}
		if org.ParentID != nil {
			if parent := nodes[*org.ParentID]; parent != nil {
				parent.children = append(parent.children, node)
				continue
			}
		}
		roots = append(roots, node)
	}

	sortNodes(roots)
	result := make([]AccessibleOrganization, 0, len(roots))
	for _, root := range roots {
		result = append(result, materializeNode(root))
	}
	return result
}

func sortNodes(nodes []*treeNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].value.Depth == nodes[j].value.Depth {
			return strings.ToLower(nodes[i].value.Name) < strings.ToLower(nodes[j].value.Name)
		}
		return nodes[i].value.Depth < nodes[j].value.Depth
	})
}

func materializeNode(node *treeNode) AccessibleOrganization {
	sortNodes(node.children)
	value := node.value
	value.Children = make([]AccessibleOrganization, 0, len(node.children))
	for _, child := range node.children {
		value.Children = append(value.Children, materializeNode(child))
	}
	return value
}

func moveSubtree(tx *gorm.DB, oldPath, newPath string, depthDelta int) error {
	if oldPath == "" {
		return fmt.Errorf("organization path is empty")
	}
	var subtree []model.Organization
	if err := tx.Where("path LIKE ?", oldPath+"%").Find(&subtree).Error; err != nil {
		return err
	}
	for _, child := range subtree {
		nextPath := strings.Replace(child.Path, oldPath, newPath, 1)
		nextDepth := child.Depth + depthDelta
		if err := tx.Model(&model.Organization{}).
			Where("id = ?", child.ID).
			Updates(map[string]any{"path": nextPath, "depth": nextDepth}).Error; err != nil {
			return err
		}
	}
	return nil
}

func childPath(parentPath string, id uint) string {
	if parentPath == "" {
		return fmt.Sprintf("/%d/", id)
	}
	return strings.TrimRight(parentPath, "/") + fmt.Sprintf("/%d/", id)
}
