package runtimerelease

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMountContractRecordsImageAliasesAndRequiredDestinations(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(root, "data", "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "image-file"), []byte("image content"), 0644); err != nil {
		t.Fatal(err)
	}
	contract, err := generateMountContract(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{"/data/alias", "/data/alias/child", "/data", "/image-file", "/image-file/child", "/etc/child", "/proc", "/usr/bin/new", "/opt", "/", "/work/../data", "/work\nspace"} {
		if err = contract.ValidateDestination("workspace", destination); err == nil {
			t.Fatal("unsafe destination accepted", destination)
		}
	}
	for _, destination := range []string{"/workspaces/project", "/state/project", "/datasets/reference", "/data/independent"} {
		if err = contract.ValidateDestination("workspace", destination); err != nil {
			t.Fatal(destination, err)
		}
	}
	if err = contract.ValidateDestination("agent_config", "/opt/agent-config"); err != nil {
		t.Fatal(err)
	}
	if err = contract.ValidateDestination("agent_config", "/other/config"); err == nil {
		t.Fatal("required publication moved")
	}
	if err = contract.ValidateDestination("data", "/opt/agent-config"); err == nil {
		t.Fatal("data shadowed required publication")
	}
	if err = contract.ValidateDestination("invented", "/extra"); err == nil {
		t.Fatal("undeclared role accepted")
	}
	contract.RequiredPublications = append(contract.RequiredPublications, RequiredPublication{Role: "reference", Destination: "/references", ArtifactID: "reference-data"})
	if err = contract.Validate(); err != nil {
		t.Fatal(err)
	}
	if err = contract.ValidateDestination("reference", "/references"); err != nil {
		t.Fatal(err)
	}
	contract.ProtectedRoots = nil
	if err = contract.Validate(); err == nil {
		t.Fatal("mandatory OS protection omitted")
	}
}
