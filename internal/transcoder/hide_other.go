//go:build !windows

package transcoder

import "os/exec"

func hideWindow(_ *exec.Cmd) {}
