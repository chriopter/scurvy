// scurvy finds the containers on this server that miss their updates, and
// updates them with Enter.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chriopter/scurvy/internal/fix"
	"github.com/chriopter/scurvy/internal/scan"
	"github.com/chriopter/scurvy/internal/tui"
	"github.com/chriopter/scurvy/internal/update"
)

var version = "dev"

const usage = `scurvy: which of your containers have scurvy?

  scurvy            the crew, sickest first; Enter updates one
  scurvy list       the same as plain text
  scurvy fix NAME   update one container (service or container name)
  scurvy update     update scurvy itself
  scurvy version

Compose labels: scurvy.ignore=true leaves a container out,
scurvy.due-after=7d sets how long its updates may wait.
`

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	var err error
	switch cmd {
	case "":
		var restart string
		if restart, err = tui.Run(version); err == nil && restart != "" {
			fmt.Println("scurvy updated itself (signature checked); starting it again")
			err = syscall.Exec(restart, os.Args, os.Environ())
		}
	case "list", "ls":
		err = list()
	case "fix":
		err = fixOne(args[1:])
	case "update":
		err = selfUpdate(false)
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

func list() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	items, err := scan.Scan(ctx, nil)
	if err != nil {
		return err
	}
	for _, it := range items {
		sign := map[scan.Level]string{scan.OK: "ok", scan.Fresh: "fresh", scan.Due: "overdue", scan.Sick: "sick"}[it.Level]
		if it.Err != "" && it.Level == scan.OK {
			sign = "unknown"
		}
		name := it.Name()
		if it.Project.Name != "" && it.Project.Name != it.Service {
			name = it.Project.Name + "/" + name
		}
		fmt.Printf("%-8s %-32s %-7s %s\n", sign, name, it.Kind, it.Image)
		for _, f := range it.Findings {
			fmt.Printf("         · %s\n", f.Text)
		}
		if it.Err != "" {
			fmt.Printf("         · could not check: %s\n", it.Err)
		}
	}
	return nil
}

// fixOne updates what the screen hands over (one container or a stack's),
// or the container named on the command line.
func fixOne(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fromScreen := os.Getenv(tui.ItemEnv) != ""
	var todo []scan.Item
	switch {
	case fromScreen:
		raw := []byte(os.Getenv(tui.ItemEnv))
		if err := json.Unmarshal(raw, &todo); err != nil {
			var one scan.Item
			if err := json.Unmarshal(raw, &one); err != nil {
				return err
			}
			todo = []scan.Item{one}
		}
	case len(args) == 1:
		items, err := scan.Scan(ctx, nil)
		if err != nil {
			return err
		}
		for _, c := range items {
			if c.Service == args[0] || c.Container.Name == args[0] {
				todo = append(todo, c)
			}
		}
		switch len(todo) {
		case 0:
			return fmt.Errorf("no running container %s", args[0])
		case 1:
		default:
			return fmt.Errorf("%s names more than one container; use the container name", args[0])
		}
	default:
		return errors.New("usage: scurvy fix NAME")
	}

	var failed error
	for i, it := range todo {
		if i > 0 {
			fmt.Println()
		}
		rollback, err := fix.Run(ctx, it, os.Stdout)
		if err == nil {
			fmt.Printf("\n\033[32m✓ %s is shipshape again\033[0m\n", it.Name())
			fmt.Printf("\033[2m  if it misbehaves: %s\033[0m\n", rollback)
			continue
		}
		fmt.Printf("\n\033[31m✗ %s: %v\033[0m\n", it.Name(), err)
		failed = err
		if ctx.Err() != nil {
			break
		}
	}
	err := failed
	if fromScreen {
		fmt.Print("\n\033[2mpress enter to go back\033[0m")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return err
}

// selfUpdate installs the newest release; restart starts it right away.
func selfUpdate(restart bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rel, err := update.Latest(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(rel.Tag, version) {
		fmt.Println("scurvy", version, "is the newest")
		return nil
	}
	path, err := update.Install(ctx, rel.Tag)
	if err != nil {
		return err
	}
	fmt.Println("installed scurvy", rel.Tag, "(signature checked)")
	if restart {
		return syscall.Exec(path, os.Args, os.Environ())
	}
	return nil
}
