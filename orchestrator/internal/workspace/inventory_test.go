package workspace

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testLister map[string]DirectoryPage

func (l testLister) List(_ context.Context, path, after string) (DirectoryPage, error) {
	p, ok := l[path+":"+after]
	if !ok {
		return DirectoryPage{}, errors.New("listing failed")
	}
	return p, nil
}
func TestInventoryCollectIsBoundedAndDoesNotTraverseSymlinks(t *testing.T) {
	lister := testLister{":": {Entries: []Entry{{Name: ".harness-upload-key-temp", Type: "file"}, {Name: "dir", Type: "directory"}}, Next: "dir"}, ":dir": {Entries: []Entry{{Name: "link", Type: "symlink"}}}, "dir:": {Entries: []Entry{{Name: "file", Type: "file", Size: 4, ModifiedAt: time.Now()}}}}
	files, err := Collect(context.Background(), lister, 10)
	if err != nil || len(files) != 1 || files[0].Path != "dir/file" {
		t.Fatalf("inventory: %+v %v", files, err)
	}
	if files, err := Collect(context.Background(), lister, 2); err == nil || files != nil {
		t.Fatal("overflow returned partial inventory")
	}
	delete(lister, "dir:")
	if files, err := Collect(context.Background(), lister, 10); err == nil || files != nil {
		t.Fatal("directory failure returned deletion evidence")
	}
}
func TestInventoryRejectsBrokenPaginationAndPaths(t *testing.T) {
	for _, page := range []DirectoryPage{{Next: "x", Entries: []Entry{{Name: "z", Type: "symlink"}}}, {Entries: []Entry{{Name: "../outside", Type: "file"}}}, {Entries: []Entry{{Name: "file", Type: "file", Size: -1, ModifiedAt: time.Now()}}}} {
		if _, err := Collect(context.Background(), testLister{":": page}, 10); err == nil {
			t.Fatalf("invalid page accepted: %+v", page)
		}
	}
}
