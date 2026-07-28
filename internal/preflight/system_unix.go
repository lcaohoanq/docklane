package preflight

import (
	"context"
	"os/exec"
)

func lookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func serviceActive(ctx context.Context, service string) (bool, error) {
	err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", service).Run()
	if err == nil {
		return true, nil
	}
	if exitError, ok := err.(*exec.ExitError); ok {
		switch exitError.ExitCode() {
		case 3, 4:
			return false, nil
		}
	}
	return false, err
}
