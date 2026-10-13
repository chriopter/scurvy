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

const usage = `scurvy: which of your containers have scurvy?

  scurvy            the containers by stack; Enter updates
  scurvy list       the same as plain text
  scurvy fix NAME   update one container, or all of a stack's
  scurvy update     update scurvy itself
  scurvy version

  -n, --dry-run     show every step of an update, run none

Compose labels: scurvy.ignore=true leaves a container out,
scurvy.due-after=7d sets how long its updates may wait.
`

func main() {
	var args []string
	for _, a := range os.Args[1:] {
		if a == "--dry-run" || a == "-n" {
			fix.DryRun = true
			continue
		}
		args = append(args, a)
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
	for _, it := range items {
		state := it.Level.Word()
		if it.Unchecked() {
			state = "unchecked"
		}
		name := it.Name()
		if it.Project.Name != "" && it.Project.Name != it.Service {
			name = it.Project.Name + "/" + name
		}
		fmt.Printf("%-9s %-32s %-10s %s\n", state, name, it.Source(), it.Image)
		for _, f := range it.Findings {
			fmt.Printf("          · %s\n", f.Text)
		}
		for _, e := range it.Errs {
			fmt.Printf("          ? %s\n", e)
		}
	}
	return nil
}

// fixNamed updates the container called NAME (its service or container
// name), or all of a stack's that need it when NAME is a stack.
func fixNamed(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: scurvy fix NAME")
	}
	items, err := scan.Scan(ctx, nil)
	if err != nil {
		return err
	}
	var todo []scan.Item
	for _, it := range items {
		if it.Service == args[0] || it.Container.Name == args[0] {
			todo = append(todo, it)
		}
	}
	if len(todo) == 0 {
		for _, it := range items {
			if it.Project.Name == args[0] && it.Level > scan.OK {
				todo = append(todo, it)
			}
		}
	}
	if len(todo) == 0 {
		return fmt.Errorf("no running container or stack with something to update called %s", args[0])
	}
	plans, refused := fix.Plans(todo)
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
