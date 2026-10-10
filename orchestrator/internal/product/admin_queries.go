package product

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"harness-platform/orchestrator/internal/sqlitex"
)

type AdminGroup struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
}
type AdminUser struct {
	Account
	LastLoginAt string       `json:"last_login_at,omitempty"`
	Groups      []AdminGroup `json:"groups"`
}
type AdminUserQuery struct {
	Search, Status, Role, Cursor string
	Limit                        int
}
type AdminUserPage struct {
	Users      []AdminUser `json:"users"`
	NextCursor string      `json:"next_cursor,omitempty"`
}
type AdminGroupPage struct {
	Groups     []AdminGroup `json:"groups"`
	NextCursor string       `json:"next_cursor,omitempty"`
}
type AuditQuery struct {
	Actor, Target, Action, After, Before, Cursor string
	Limit                                        int
}
type AuditEvent struct {
	ID         int64           `json:"id"`
	ActorID    string          `json:"actor_id"`
	Action     string          `json:"action"`
	TargetKind string          `json:"target_kind"`
	TargetID   string          `json:"target_id"`
	RequestID  string          `json:"request_id,omitempty"`
	Revision   int64           `json:"revision"`
	Details    json.RawMessage `json:"details"`
	CreatedAt  string          `json:"created_at"`
}
type AuditPage struct {
	Events     []AuditEvent `json:"events"`
	NextCursor string       `json:"next_cursor,omitempty"`
}
type adminCursor struct {
	Last  string `json:"last"`
	Scope string `json:"scope"`
}

func cursorScope(kind string, filter any) string {
	request, _ := mutationRequest("cursor", kind, "", filter)
	return request.Digest
}
func readAdminCursor(raw, scope string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 1024 {
		return "", ErrInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", Admission("cursor_invalid", ErrInvalid)
	}
	var cursor adminCursor
	if err = json.Unmarshal(decoded, &cursor); err != nil || cursor.Scope != scope || len(cursor.Last) > 128 {
		return "", Admission("cursor_invalid", ErrInvalid)
	}
	return cursor.Last, nil
}
func nextAdminCursor(last, scope string) string {
	raw, _ := json.Marshal(adminCursor{last, scope})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func adminLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > 100 {
		return 0, Admission("page_limit_invalid", ErrInvalid)
	}
	return limit, nil
}

const adminUserSelect = `SELECT a.id,a.username,a.display_name,a.status,a.role,a.revision,a.session_revision,a.must_change_password,a.created_at,a.updated_at,a.last_login_at,
 json_group_array(json_object('id',g.id,'name',g.name,'revision',g.revision)) FILTER (WHERE g.id IS NOT NULL)
 FROM accounts a LEFT JOIN account_memberships m ON m.account_id=a.id LEFT JOIN account_groups g ON g.id=m.group_id `

func scanAdminUser(row scanner) (u AdminUser, err error) {
	var raw string
	err = missing(row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Status, &u.Role, &u.Revision, &u.SessionRevision, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt, &raw))
	if err != nil {
		return u, err
	}
	err = json.Unmarshal([]byte(raw), &u.Groups)
	sort.Slice(u.Groups, func(i, j int) bool { return u.Groups[i].ID < u.Groups[j].ID })
	return
}
func adminUser(ctx context.Context, q queryer, id string) (AdminUser, error) {
	return scanAdminUser(q.QueryRowContext(ctx, adminUserSelect+`WHERE a.id=? GROUP BY a.id`, id))
}
func (s *Store) AdminUser(ctx context.Context, token, id string) (AdminUser, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return AdminUser{}, err
	}
	return adminUser(ctx, s.reader, id)
}
func (s *Store) AdminUsers(ctx context.Context, token string, filter AdminUserQuery) (AdminUserPage, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return AdminUserPage{}, err
	}
	limit, err := adminLimit(filter.Limit)
	if err != nil {
		return AdminUserPage{}, err
	}
	if !utf8.ValidString(filter.Search) || len(filter.Search) > 512 || strings.ContainsRune(filter.Search, 0) || filter.Status != "" && filter.Status != "active" && filter.Status != "disabled" || filter.Role != "" && filter.Role != "admin" && filter.Role != "user" {
		return AdminUserPage{}, ErrInvalid
	}
	filter.Search = strings.TrimSpace(filter.Search)
	scope := cursorScope("users", []string{filter.Search, filter.Status, filter.Role})
	after, err := readAdminCursor(filter.Cursor, scope)
	if err != nil {
		return AdminUserPage{}, err
	}
	rows, err := s.reader.QueryContext(ctx, adminUserSelect+`WHERE a.id>? AND (?='' OR instr(lower(a.username),lower(?))>0 OR instr(lower(a.display_name),lower(?))>0) AND (?='' OR a.status=?) AND (?='' OR a.role=?) GROUP BY a.id ORDER BY a.id LIMIT ?`, after, filter.Search, filter.Search, filter.Search, filter.Status, filter.Status, filter.Role, filter.Role, limit+1)
	if err != nil {
		return AdminUserPage{}, err
	}
	defer rows.Close()
	result := AdminUserPage{Users: []AdminUser{}}
	for rows.Next() {
		user, err := scanAdminUser(rows)
		if err != nil {
			return result, err
		}
		result.Users = append(result.Users, user)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Users) > limit {
		result.Users = result.Users[:limit]
		result.NextCursor = nextAdminCursor(result.Users[limit-1].ID, scope)
	}
	return result, nil
}
func (s *Store) AdminGroups(ctx context.Context, token, cursor string, limit int) (AdminGroupPage, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return AdminGroupPage{}, err
	}
	limit, err := adminLimit(limit)
	if err != nil {
		return AdminGroupPage{}, err
	}
	scope := cursorScope("groups", nil)
	after, err := readAdminCursor(cursor, scope)
	if err != nil {
		return AdminGroupPage{}, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT id,name,revision FROM account_groups WHERE id>? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		return AdminGroupPage{}, err
	}
	defer rows.Close()
	result := AdminGroupPage{Groups: []AdminGroup{}}
	for rows.Next() {
		var group AdminGroup
		if err = rows.Scan(&group.ID, &group.Name, &group.Revision); err != nil {
			return result, err
		}
		result.Groups = append(result.Groups, group)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Groups) > limit {
		result.Groups = result.Groups[:limit]
		result.NextCursor = nextAdminCursor(result.Groups[limit-1].ID, scope)
	}
	return result, nil
}
func (s *Store) AdminAudit(ctx context.Context, token string, filter AuditQuery) (AuditPage, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return AuditPage{}, err
	}
	limit, err := adminLimit(filter.Limit)
	if err != nil {
		return AuditPage{}, err
	}
	for _, value := range []string{filter.Actor, filter.Target, filter.Action} {
		if len(value) > 128 || strings.ContainsRune(value, 0) {
			return AuditPage{}, ErrInvalid
		}
	}
	for _, value := range []*string{&filter.After, &filter.Before} {
		if *value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, *value)
			if err != nil {
				return AuditPage{}, ErrInvalid
			}
			*value = sqlitex.FormatTime(parsed)
		}
	}
	if filter.After != "" && filter.Before != "" && filter.After > filter.Before {
		return AuditPage{}, ErrInvalid
	}
	scope := cursorScope("audit", []string{filter.Actor, filter.Target, filter.Action, filter.After, filter.Before})
	last, err := readAdminCursor(filter.Cursor, scope)
	if err != nil {
		return AuditPage{}, err
	}
	after := int64(0)
	if last != "" {
		after, err = strconv.ParseInt(last, 10, 64)
		if err != nil || after < 1 {
			return AuditPage{}, ErrInvalid
		}
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT id,actor_id,action,target_kind,target_id,request_id,revision,details,created_at FROM audit_events WHERE id>? AND (?='' OR actor_id=?) AND (?='' OR target_id=?) AND (?='' OR action=?) AND (?='' OR created_at>=?) AND (?='' OR created_at<=?) ORDER BY id LIMIT ?`, after, filter.Actor, filter.Actor, filter.Target, filter.Target, filter.Action, filter.Action, filter.After, filter.After, filter.Before, filter.Before, limit+1)
	if err != nil {
		return AuditPage{}, err
	}
	defer rows.Close()
	result := AuditPage{Events: []AuditEvent{}}
	for rows.Next() {
		var event AuditEvent
		var raw string
		if err = rows.Scan(&event.ID, &event.ActorID, &event.Action, &event.TargetKind, &event.TargetID, &event.RequestID, &event.Revision, &raw, &event.CreatedAt); err != nil {
			return result, err
		}
		event.Details = json.RawMessage(raw)
		result.Events = append(result.Events, event)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Events) > limit {
		result.Events = result.Events[:limit]
		result.NextCursor = nextAdminCursor(strconv.FormatInt(result.Events[limit-1].ID, 10), scope)
	}
	return result, nil
}
func (s *Store) AdminOperation(ctx context.Context, token, id string) (AdminOperation, error) {
	if _, err := s.adminSession(ctx, s.reader, token); err != nil {
		return AdminOperation{}, err
	}
	var result AdminOperation
	err := missing(s.reader.QueryRowContext(ctx, `SELECT o.id,o.actor_id,o.action,o.target_kind,o.target_id,o.state,o.error,o.created_at,o.updated_at,count(t.project_id),coalesce(sum(t.state='succeeded'),0) FROM admin_operations o LEFT JOIN admin_operation_targets t ON t.operation_id=o.id WHERE o.id=? GROUP BY o.id`, id).Scan(&result.ID, &result.ActorID, &result.Action, &result.TargetKind, &result.TargetID, &result.State, &result.Error, &result.CreatedAt, &result.UpdatedAt, &result.Total, &result.Completed))
	return result, err
}
