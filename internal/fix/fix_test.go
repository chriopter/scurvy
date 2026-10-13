package fix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chriopter/scurvy/internal/docker"
	"github.com/chriopter/scurvy/internal/scan"
)

func TestEditAndRestore(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	orig := "services:\n  a:\n    image: app:1.0\n  b:\n    image: app:1.1\n"
	os.WriteFile(p, []byte(orig), 0o640)
	e := scan.Edit{File: p, Line: 3, Old: "    image: app:1.0", New: "    image: app:1.1"}

	files, err := edit([]scan.Edit{e}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != strings.Replace(orig, "app:1.0", "app:1.1", 1) {
		t.Fatalf("edited:\n%s", got)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v", st.Mode())
	}
	if err := restore(files); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != orig {
		t.Fatalf("restore must give back exactly the original, b's 1.1 included:\n%s", got)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".*scurvy*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestEditRefuses(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	os.WriteFile(p, []byte("image: app:2.0\n"), 0o644)
	if _, err := edit([]scan.Edit{{File: p, Line: 1, Old: "image: app:1.0", New: "x"}}, true); err == nil {
		t.Error("a line that changed since the scan must not be edited")
	}
	link := filepath.Join(dir, "link.yaml")
	os.Symlink(p, link)
	if _, err := edit([]scan.Edit{{File: link, Line: 1, Old: "image: app:2.0", New: "x"}}, true); err == nil {
		t.Error("a symlink must not be followed")
	}
	if got, _ := os.ReadFile(p); string(got) != "image: app:2.0\n" {
		t.Errorf("nothing may change: %s", got)
	}
}

func TestRestoreLeavesLaterChanges(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	os.WriteFile(p, []byte("a\n"), 0o644)
	files, _ := edit([]scan.Edit{{File: p, Line: 1, Old: "a", New: "b"}}, true)
	os.WriteFile(p, []byte("someone else\n"), 0o644)
	if err := restore(files); err == nil {
		t.Error("a file changed meanwhile must be reported, not overwritten")
	}
	if got, _ := os.ReadFile(p); string(got) != "someone else\n" {
		t.Errorf("overwritten: %s", got)
	}
}

func TestPlans(t *testing.T) {
	proj := docker.Project{Name: "shop", Dir: "/srv/shop", Files: []string{"/srv/shop/compose.yaml"}}
	e := scan.Edit{File: "/srv/shop/compose.yaml", Line: 3, Old: "x", New: "y"}
	items := []scan.Item{
		{Kind: scan.Pulled, Project: proj, Service: "web", Image: "web:1", Edits: []scan.Edit{e}},
		{Kind: scan.Pulled, Project: proj, Service: "web", Image: "web:1", Edits: []scan.Edit{e}}, // a replica
		{Kind: scan.Built, Project: proj, Service: "worker", Image: "shop-worker"},
		{Kind: scan.Loose, Container: docker.Container{Name: "aio"}, Project: docker.Project{Name: "aio"}},
		{Kind: scan.Pulled, Project: proj, Service: "db", Image: "postgres:16@sha256:abc"},
	}
	plans, refused := Plans(items)
	if len(plans) != 1 || len(plans[0].Items) != 2 || len(plans[0].Edits) != 1 {
		t.Fatalf("plans: %+v", plans)
	}
	if len(refused) != 2 {
		t.Errorf("refused: %v", refused)
	}
	items[0].Hook = "/srv/shop/docker-update.sh"
	plans, _ = Plans(items)
	if plans[0].Hook == "" || len(plans[0].Edits) != 0 {
		t.Errorf("a stack's script is in charge: no edits of ours: %+v", plans[0])
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":         "plain",
		"/srv/my stack": "'/srv/my stack'",
		"a;rm -rf /":    "'a;rm -rf /'",
		"it's":          `'it'\''s'`,
		"$(x)":          "'$(x)'",
	} {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestTagSafe(t *testing.T) {
	if got := tagSafe("my service/1"); got != "my-service-1" {
		t.Errorf("got %q", got)
	}
}
