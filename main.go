// scurvy finds the containers on this server that miss their updates, and
// updates them with Enter.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chriopter/scurvy/internal/fix"
	"github.com/chriopter/scurvy/internal/scan"
	"github.com/chriopter/scurvy/internal/tui"
	"github.com/chriopter/scurvy/internal/update"
)

var version = "dev"

var (
	rebuild bool // fix: build from scratch
	influx  bool // list: as InfluxDB line protocol, for Telegraf
)

const usage = `scurvy: which of your containers have scurvy?

  scurvy            the containers by stack; Enter updates
  scurvy list       the update state of every container, as text
  scurvy list --influx   the same as InfluxDB lines, for Telegraf
  scurvy fix NAME   update one container (or stack/service), or a stack
  scurvy update     update scurvy itself
  scurvy version

  -n, --dry-run     show every change of an update, make none
  --no-cache        fix: build from scratch

Compose labels: scurvy.ignore=true leaves a container out,
scurvy.due-after=7d sets how long its updates may wait.
`

func main() {
	var args []string
	for _, a := range os.Args[1:] {
		switch a {
		case "--dry-run", "-n":
			fix.DryRun = true
		case "--no-cache":
			rebuild = true
		case "--influx":
			influx = true
		default:
			args = append(args, a)
		}
	}
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "":
		host, _ := os.Hostname()
		var restart string
		if restart, err = tui.Run(ctx, host, version, fix.DryRun); err == nil && restart != "" {
			fmt.Println("scurvy updated itself (signature checked); starting it again")
			err = syscall.Exec(restart, os.Args, os.Environ())
		}
	case "list", "ls":
		err = list(ctx)
	case "fix":
		err = fixNamed(ctx, args[1:])
	case "update":
		err = selfUpdate(ctx)
	case "version", "--version", "-v":
		fmt.Println("scurvy", version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "scurvy:", err)
		os.Exit(1)
	}
}

func list(ctx context.Context) error {
	items, err := scan.Scan(ctx, nil)
	if err != nil {
		return err
	}
	if influx {
		fmt.Print(lineProtocol(items))
		return nil
	}
	for _, it := range items {
		name := it.Name()
		if it.Project.Name != "" && it.Project.Name != it.Service {
			name = it.Project.Name + "/" + name
		}
		fmt.Printf("%-16s %-32s %-10s %s\n", state(it), name, it.Source(), it.Image)
		for _, f := range it.Findings {
			fmt.Printf("                 · %s\n", f.Text)
		}
		for _, e := range it.Errs {
			fmt.Printf("                 ? %s\n", e)
		}
	}
	return nil
}

// state is a container's state in the words of the screen.
func state(it scan.Item) string {
	if it.Unchecked() {
		return "not checked"
	}
	return map[scan.Level]string{scan.OK: "up to date", scan.Update: "update available",
		scan.Overdue: "update overdue", scan.EOL: "end of life"}[it.Level]
}

// lineProtocol is one InfluxDB line per container, for Telegraf's exec
// input: tags stack, container, type; fields state, level (0 up to date …
// 3 end of life, -1 not checked), waiting_days and running (1 or 0).
func lineProtocol(items []scan.Item) string {
	tag := strings.NewReplacer(",", `\,`, " ", `\ `, "=", `\=`)
	var b strings.Builder
	for _, it := range items {
		level := int(it.Level)
		if it.Unchecked() {
			level = -1
		}
		waiting := 0
		if f, ok := it.Top(); ok && !f.Since.IsZero() {
			waiting = int(time.Since(f.Since).Hours() / 24)
		}
		stack := it.Project.Name
		if stack == "" {
			stack = "-"
		}
		running := 0
		if it.Up() {
			running = 1
		}
		fmt.Fprintf(&b, "scurvy,stack=%s,container=%s,type=%s state=%q,level=%di,waiting_days=%di,running=%di\n",
			tag.Replace(stack), tag.Replace(it.Name()), tag.Replace(it.Source()), state(it), level, waiting, running)
	}
	return b.String()
}

// fixNamed updates what NAME names: a container (its service or container
// name, or project/service when a name is not unique), or the containers
// of a stack that need it.
func fixNamed(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: scurvy fix NAME")
	}
	items, err := scan.Scan(ctx, nil)
	if err != nil {
		return err
	}
	name := args[0]
	var todo []scan.Item
	for _, it := range items {
		if it.Service == name || it.Container.Name == name || it.Project.Name+"/"+it.Service == name {
			todo = append(todo, it)
		}
	}
	projects := map[string]bool{}
	for _, it := range todo {
		projects[it.Project.Name] = true
	}
	if len(projects) > 1 {
		var names []string
		for _, it := range todo {
			names = append(names, it.Project.Name+"/"+it.Service)
		}
		return fmt.Errorf("%s is in more than one stack; say which: %s", name, strings.Join(names, ", "))
	}
	if len(todo) == 0 {
		for _, it := range items {
			if it.Project.Name == name && it.Level > scan.OK {
				todo = append(todo, it)
			}
		}
	}
	if len(todo) == 0 {
		return fmt.Errorf("no running container or stack with something to update called %s", name)
	}
	plans, refused := fix.Plans(todo, items, rebuild)
	if len(refused) > 0 {
		return errors.New(strings.Join(refused, "; "))
	}
	for _, p := range plans {
		if err := fix.Run(ctx, p, os.Stdout); err != nil {
			return err
		}
	}
	return nil
}

// selfUpdate installs the newest release.
func selfUpdate(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	rel, err := update.Latest(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(rel.Tag, version) {
		fmt.Println("scurvy", version, "is the newest")
		return nil
	}
	if fix.DryRun {
		fmt.Println("dry run: would install scurvy", rel.Tag)
		return nil
	}
	if _, err := update.Install(ctx, rel.Tag); err != nil {
		return err
	}
	fmt.Println("installed scurvy", rel.Tag, "(signature checked)")
	return nil
}
