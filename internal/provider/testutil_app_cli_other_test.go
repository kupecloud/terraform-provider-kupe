//go:build !linux

package provider

import "os/exec"

// Non-Linux terraform-exec uses ordinary context-owned child cleanup too.
func configureAppCLIProcess(_ *exec.Cmd) {}

func runAppCLIPlatformFixture() (bool, int) { return false, 0 }
