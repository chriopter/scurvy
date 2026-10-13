package fix

import (
	"context"
	"errors"
	"io"
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
	plans, refused := Plans(items, items, false)
	if len(plans) != 1 || len(plans[0].Items) != 2 || len(plans[0].Edits) != 1 {
		t.Fatalf("plans: %+v", plans)
	}
	if len(refused) != 2 {
		t.Errorf("refused: %v", refused)
	}
	items[0].Hook = "/srv/shop/docker-update.sh"
	plans, _ = Plans(items, items, false)
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

// fake records the commands a plan runs and fails the one that starts
// with failAt.
type fake struct {
	ran    []string
	failAt string
}

func (f *fake) exec(_ context.Context, _ io.Writer, _ string, name string, args ...string) error {
	line := name + " " + strings.Join(args, " ")
	f.ran = append(f.ran, line)
	if f.failAt != "" && strings.Contains(line, f.failAt) {
		f.failAt = "" // the way back succeeds
		return errors.New("boom")
	}
	return nil
}

func (f *fake) has(t *testing.T, want ...string) {
	t.Helper()
	i := 0
	for _, l := range f.ran {
		if i < len(want) && strings.Contains(l, want[i]) {
			i++
		}
	}
	if i < len(want) {
		t.Fatalf("missing %q in order; ran:\n%s", want[i], strings.Join(f.ran, "\n"))
	}
}

func (f *fake) count(s string) int {
	n := 0
	for _, l := range f.ran {
		if strings.Contains(l, s) {
			n++
		}
	}
	return n
}

func plan(t *testing.T) (Plan, string) {
	lockFile = filepath.Join(t.TempDir(), "scurvy.lock")
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	os.WriteFile(p, []byte("services:\n  web:\n    image: app:1.0\n"), 0o644)
	it := scan.Item{
		Kind: scan.Pulled, Service: "web", Image: "app:1.0", Running: "app:1.0",
		Container: docker.Container{ImageID: "sha256:old"},
		Project:   docker.Project{Name: "shop", Dir: dir, Files: []string{p}},
		Edits:     []scan.Edit{{File: p, Line: 3, Old: "    image: app:1.0", New: "    image: app:1.1"}},
	}
	plans, _ := Plans([]scan.Item{it}, nil, false)
	return plans[0], p
}

func TestRunGoesWell(t *testing.T) {
	p, file := plan(t)
	f := &fake{}
	if err := run(&runner{ctx: context.Background(), out: io.Discard, exec: f.exec}, p); err != nil {
		t.Fatal(err)
	}
	f.has(t, "docker tag sha256:old app:bak-", "pull web", "up -d --no-deps --pull never --no-build --wait")
	if got, _ := os.ReadFile(file); !strings.Contains(string(got), "app:1.1") {
		t.Error("the version was not raised")
	}
}

func TestRunPutsBackWhenUnhealthy(t *testing.T) {
	p, file := plan(t)
	f := &fake{failAt: "up -d"}
	if err := run(&runner{ctx: context.Background(), out: io.Discard, exec: f.exec}, p); err == nil {
		t.Fatal("an unhealthy start must fail")
	}
	f.has(t, "pull web", "up -d", "docker tag app:bak-", "up -d")
	if got, _ := os.ReadFile(file); !strings.Contains(string(got), "app:1.0") {
		t.Error("the compose file was not put back")
	}
}

func TestRunLeavesContainersAloneBeforeStart(t *testing.T) {
	p, file := plan(t)
	f := &fake{failAt: "pull web"}
	if err := run(&runner{ctx: context.Background(), out: io.Discard, exec: f.exec}, p); err == nil {
		t.Fatal("a failed pull must fail")
	}
	if f.count("up -d") != 0 {
		t.Error("no container was touched yet: none must be restarted")
	}
	f.has(t, "docker tag app:bak-") // the tag goes back all the same
	if got, _ := os.ReadFile(file); !strings.Contains(string(got), "app:1.0") {
		t.Error("the compose file was not put back")
	}
}

func TestRunPutsBackWhenInterrupted(t *testing.T) {
	p, _ := plan(t)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fake{}
	exec := func(c context.Context, o io.Writer, d, n string, a ...string) error {
		if strings.Contains(strings.Join(a, " "), "up -d") && c.Err() == nil && ctx.Err() == nil {
			cancel() // Ctrl+C while the containers start
			return errors.New("interrupted")
		}
		if c.Err() != nil {
			return c.Err()
		}
		return f.exec(c, o, d, n, a...)
	}
	if err := run(&runner{ctx: ctx, out: io.Discard, exec: exec}, p); err == nil {
		t.Fatal("an interrupted update must fail")
	}
	f.has(t, "docker tag app:bak-", "up -d") // the way back ran despite the cancel
}

func TestBackupsGoBackToBothNames(t *testing.T) {
	p, _ := plan(t)
	p.Items[0].Running = "app:0.9" // the compose file already says 1.0
	f := &fake{failAt: "up -d"}
	_ = run(&runner{ctx: context.Background(), out: io.Discard, exec: f.exec}, p)
	if f.count(" app:0.9") == 0 || f.count(" app:1.0") == 0 {
		t.Errorf("the backup must go back to the name it ran as and the name compose starts:\n%s", strings.Join(f.ran, "\n"))
	}
}

func TestPlansAIO(t *testing.T) {
	aio := docker.Project{Name: "nextcloud-aio"}
	var items []scan.Item
	for _, n := range []string{"nextcloud-aio-apache", "nextcloud-aio-database"} {
		items = append(items, scan.Item{Kind: scan.AIO, Project: aio, Container: docker.Container{Name: n}, Image: "x:latest"})
	}
	plans, refused := Plans(items[:1], items, false)
	if len(refused) != 0 || len(plans) != 1 || !plans[0].AIO || len(plans[0].Items) != 2 {
		t.Fatalf("AIO updates as a whole: %+v %v", plans, refused)
	}
}
