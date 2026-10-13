package fix

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/chriopter/scurvy/internal/scan"
)

// Nextcloud All-in-One starts its containers itself and keeps their
// versions together, so scurvy does not swap them: it asks AIO's
// mastercontainer to update, the way AIO documents for scripts
// (/daily-backup.sh with AUTOMATIC_UPDATES=1), first with AIO's own backup
// if one is set up. The script returns no error codes, so scurvy watches
// the containers instead. There is no way back but AIO's backup.

const aioWait = 20 * time.Minute

func runAIO(r *runner, p Plan) error {
	if !DryRun {
		unlock, err := lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	var names []string
	for _, it := range p.Items {
		names = append(names, it.Container.Name)
	}
	r.say("updating Nextcloud All-in-One: its mastercontainer updates " + strings.Join(names, ", "))
	backup := aioBackupSet(r)
	update := []string{"exec", "--env", "AUTOMATIC_UPDATES=1"}
	if backup {
		update = append(update, "--env", "DAILY_BACKUP=1")
		r.say("AIO makes its backup first")
	} else {
		r.say("\033[33m! AIO has no backup set up: it updates without one, and scurvy cannot go back\033[0m")
	}
	update = append(update, scan.Mastercontainer, "/daily-backup.sh")

	before := containerID(r, scan.Mastercontainer)
	err := r.run("", "docker", update...)
	if DryRun {
		r.say("\033[33m· dry run: nothing was changed\033[0m")
		return nil
	}
	// When AIO updates its mastercontainer, the script stops with it; AIO
	// says to run it again once the new one is up.
	if err != nil || containerID(r, scan.Mastercontainer) != before {
		r.say("the mastercontainer was renewed; waiting for it, then once more")
		if err := r.waitHealthy([]string{scan.Mastercontainer}); err != nil {
			return r.aioFailed(err, backup)
		}
		if err := r.run("", "docker", update...); err != nil {
			return r.aioFailed(err, backup)
		}
	}
	if err := r.waitHealthy(append(names, scan.Mastercontainer)); err != nil {
		return r.aioFailed(err, backup)
	}
	r.say("\033[32m✓ Nextcloud All-in-One is shipshape again\033[0m")
	return nil
}

func (r *runner) aioFailed(err error, backup bool) error {
	r.say("\033[31m✗ " + err.Error() + "\033[0m")
	if backup {
		r.say("AIO made a backup before; restore it in the AIO interface if Nextcloud does not come back")
	} else {
		r.say("look at the AIO interface (port 8080): it shows what each container does")
	}
	return err
}

// aioBackupSet reports whether AIO has a backup location set up.
func aioBackupSet(r *runner) bool {
	out, err := exec.CommandContext(r.ctx, "docker", "exec", scan.Mastercontainer,
		"cat", "/mnt/docker-aio-config/data/configuration.json").Output()
	if err != nil {
		return false
	}
	var cfg struct {
		Location string `json:"borg_backup_host_location"`
	}
	return json.Unmarshal(out, &cfg) == nil && cfg.Location != ""
}

func containerID(r *runner, name string) string {
	out, _ := exec.CommandContext(r.ctx, "docker", "inspect", "-f", "{{.Id}}", name).Output()
	return strings.TrimSpace(string(out))
}

// waitHealthy waits until every named container runs, and passes its
// health check if it has one.
func (r *runner) waitHealthy(names []string) error {
	deadline := time.Now().Add(aioWait)
	for {
		pending := ""
		for _, n := range names {
			out, err := exec.CommandContext(r.ctx, "docker", "inspect", "-f",
				"{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}healthy{{end}}", n).Output()
			if state := strings.TrimSpace(string(out)); err != nil || state != "running healthy" {
				pending = n
				break
			}
		}
		if pending == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not come up healthy within %s", pending, aioWait)
		}
		select {
		case <-r.ctx.Done():
			return errors.New("interrupted; AIO goes on by itself, see its interface")
		case <-time.After(5 * time.Second):
		}
	}
}
