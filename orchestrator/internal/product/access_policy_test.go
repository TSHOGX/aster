package product

import "testing"

func TestAccessDenyPrecedenceAndProvenance(t *testing.T) {
	allow := PolicyBinding{GroupID: "allowed", GroupName: "Allowed", Policy: PermissionPolicy{ID: "p1", Statements: map[ProductAction]PolicyEffect{ActionFilesRead: PolicyAllow}}}
	deny := PolicyBinding{GroupID: "denied", GroupName: "Denied", Policy: PermissionPolicy{ID: "p2", Statements: map[ProductAction]PolicyEffect{ActionFilesRead: PolicyDeny}}}
	unset := PolicyBinding{GroupID: "unset", Policy: PermissionPolicy{ID: "p3", Statements: map[ProductAction]PolicyEffect{ActionFilesRead: PolicyUnset}}}
	for _, tc := range []struct {
		bindings []PolicyBinding
		allowed  bool
		sources  int
	}{{nil, false, 0}, {[]PolicyBinding{allow, unset}, true, 1}, {[]PolicyBinding{allow, deny, unset}, false, 2}, {[]PolicyBinding{deny, allow}, false, 2}} {
		result, err := ResolveAccess(tc.bindings)
		if err != nil || result[ActionFilesRead].Allowed != tc.allowed || len(result[ActionFilesRead].Sources) != tc.sources {
			t.Fatal(result, err)
		}
		if result[ActionFilesExport].Allowed || result[ActionAdminContentRead].Allowed {
			t.Fatal("missing grant authorized action")
		}
	}
	if err := ValidateStatements(map[ProductAction]PolicyEffect{"files.*": PolicyAllow}); err == nil {
		t.Fatal("wildcard granted future authority")
	}
}
