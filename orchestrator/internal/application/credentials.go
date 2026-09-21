package application

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
	"harness-platform/orchestrator/internal/auth"
	"harness-platform/orchestrator/internal/producthttp"
	"harness-platform/orchestrator/internal/schemastate"
)

// UpdateUser atomically edits only the credential registry. Cookie revocation is
// derived from its fingerprint by the running application, not a second DB writer.
func UpdateUser(name, action, username, password, role string) error {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return err
	}
	unlock, err := schemastate.LockPaths(absolute)
	if err != nil {
		return err
	}
	defer unlock()
	users, err := ReadUsers(absolute)
	if os.IsNotExist(err) && action == "add" {
		users = nil
		err = nil
	}
	if err != nil {
		return err
	}
	index := -1
	for n, user := range users {
		if user.Username == username {
			index = n
			break
		}
	}
	switch action {
	case "add":
		if index >= 0 {
			return errors.New("user already exists")
		}
		if len(password) < 8 || len(password) > 4096 {
			return errors.New("password must contain 8 to 4096 bytes")
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return err
		}
		if role == "" {
			role = "user"
		}
		users = append(users, producthttp.AuthUser{Username: username, PasswordHash: hash, Role: role})
	case "password":
		if index < 0 {
			return errors.New("user not found")
		}
		if len(password) < 8 || len(password) > 4096 {
			return errors.New("password must contain 8 to 4096 bytes")
		}
		users[index].PasswordHash, err = auth.HashPassword(password)
		if err != nil {
			return err
		}
	case "role":
		if index < 0 {
			return errors.New("user not found")
		}
		users[index].Role = role
	case "remove":
		if index < 0 {
			return errors.New("user not found")
		}
		users = append(users[:index], users[index+1:]...)
	default:
		return fmt.Errorf("unknown credential action %q", action)
	}
	checker := &producthttp.Authentication{}
	if err = checker.ReplaceUsers(users); err != nil {
		return err
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	raw, err := yaml.Marshal(struct {
		Users []producthttp.AuthUser `yaml:"users"`
	}{users})
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(absolute), ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(raw); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temp.Name(), absolute); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(absolute))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
