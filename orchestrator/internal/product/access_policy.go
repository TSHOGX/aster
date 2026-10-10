package product

import "sort"

type ProductAction string

const (
	ActionProjectsRead       ProductAction = "projects.read"
	ActionProjectsCreate     ProductAction = "projects.create"
	ActionProjectsManage     ProductAction = "projects.manage"
	ActionFilesList          ProductAction = "files.list"
	ActionFilesRead          ProductAction = "files.read"
	ActionFilesExport        ProductAction = "files.export"
	ActionFilesUpload        ProductAction = "files.upload"
	ActionFilesEdit          ProductAction = "files.edit"
	ActionFilesManage        ProductAction = "files.manage"
	ActionConversationsShare ProductAction = "conversations.share"
	ActionPublicSharesCreate ProductAction = "shares.public.create"
	ActionTerminalsUse       ProductAction = "terminals.use"
	ActionAgentsRun          ProductAction = "agents.run"
	ActionSchedulesManage    ProductAction = "schedules.manage"
	ActionAdminContentRead   ProductAction = "admin.content.read"
)

var productActions = []ProductAction{ActionProjectsRead, ActionProjectsCreate, ActionProjectsManage, ActionFilesList, ActionFilesRead, ActionFilesExport, ActionFilesUpload, ActionFilesEdit, ActionFilesManage, ActionConversationsShare, ActionPublicSharesCreate, ActionTerminalsUse, ActionAgentsRun, ActionSchedulesManage, ActionAdminContentRead}

type PolicyEffect string

const (
	PolicyAllow PolicyEffect = "allow"
	PolicyDeny  PolicyEffect = "deny"
	PolicyUnset PolicyEffect = "unset"
)

type PermissionPolicy struct {
	ID          string                         `json:"id"`
	Name        string                         `json:"name"`
	Description string                         `json:"description"`
	Revision    int64                          `json:"revision"`
	Statements  map[ProductAction]PolicyEffect `json:"statements"`
}
type PolicyBinding struct {
	GroupID   string           `json:"group_id"`
	GroupName string           `json:"group_name"`
	Policy    PermissionPolicy `json:"policy"`
}
type AccessSource struct {
	GroupID    string       `json:"group_id"`
	GroupName  string       `json:"group_name"`
	PolicyID   string       `json:"policy_id"`
	PolicyName string       `json:"policy_name"`
	Effect     PolicyEffect `json:"effect"`
}
type EffectiveAction struct {
	Allowed bool           `json:"allowed"`
	Sources []AccessSource `json:"sources"`
}

func validProductAction(action ProductAction) bool {
	for _, known := range productActions {
		if action == known {
			return true
		}
	}
	return false
}
func ValidateStatements(statements map[ProductAction]PolicyEffect) error {
	for action, effect := range statements {
		if !validProductAction(action) || effect != PolicyAllow && effect != PolicyDeny && effect != PolicyUnset {
			return Admission("policy_invalid", ErrInvalid)
		}
	}
	return nil
}
func ResolveAccess(bindings []PolicyBinding) (map[ProductAction]EffectiveAction, error) {
	for _, b := range bindings {
		if err := ValidateStatements(b.Policy.Statements); err != nil {
			return nil, err
		}
	}
	result := make(map[ProductAction]EffectiveAction, len(productActions))
	for _, action := range productActions {
		value := EffectiveAction{Sources: []AccessSource{}}
		denied := false
		for _, b := range bindings {
			effect := b.Policy.Statements[action]
			if effect == PolicyAllow || effect == PolicyDeny {
				value.Allowed = value.Allowed || effect == PolicyAllow
				denied = denied || effect == PolicyDeny
				value.Sources = append(value.Sources, AccessSource{b.GroupID, b.GroupName, b.Policy.ID, b.Policy.Name, effect})
			}
		}
		value.Allowed = value.Allowed && !denied
		sort.Slice(value.Sources, func(i, j int) bool {
			a, b := value.Sources[i], value.Sources[j]
			if a.GroupID != b.GroupID {
				return a.GroupID < b.GroupID
			}
			return a.PolicyID < b.PolicyID
		})
		result[action] = value
	}
	return result, nil
}
func standardStatements() map[ProductAction]PolicyEffect {
	result := map[ProductAction]PolicyEffect{}
	for _, action := range productActions {
		if action != ActionPublicSharesCreate && action != ActionAdminContentRead {
			result[action] = PolicyAllow
		}
	}
	return result
}
