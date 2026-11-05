//go:build linux

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Preserve terraform-exec's Linux policy for the actual CLI behind the observer.
func configureAppCLIProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
}

const appCLILifetimeModeEnv = "KUPE_APP_TEST_LIFETIME_MODE"
const appCLILifetimeReadyEnv = "KUPE_APP_TEST_LIFETIME_READY"

type appCLIOwnedProcess struct {
	PID, ParentPID   int
	StartTick, State string
}

type appCLILifetimeReady struct{ Child, Observer appCLIOwnedProcess }

func runAppCLIPlatformFixture() (bool, int) {
	switch os.Getenv(appCLILifetimeModeEnv) {
	case "block":
		child, err := readAppCLIProcess(os.Getpid())
		if err != nil {
			return appCLILifetimeFailure(err)
		}
		observer, err := readAppCLIProcess(os.Getppid())
		if err != nil {
			return appCLILifetimeFailure(err)
		}
		data, err := json.Marshal(appCLILifetimeReady{Child: child, Observer: observer})
		if err != nil {
			return appCLILifetimeFailure(err)
		}
		if err := os.WriteFile(os.Getenv(appCLILifetimeReadyEnv), data, 0600); err != nil {
			return appCLILifetimeFailure(err)
		}
		// A broken cleanup control still has a finite lifetime; the proof must
		// detect death within three seconds, well before this fixture ends.
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		<-timer.C
		return true, 0
	case "runner":
		executable, err := os.Executable()
		if err != nil {
			return appCLILifetimeFailure(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "app-lifetime-block")
		cmd.Env = appCLILifetimeEnv("block", true, executable, os.Getenv(appCLILifetimeReadyEnv))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		configureAppCLIProcess(cmd)
		if err := cmd.Run(); err != nil {
			return appCLILifetimeFailure(err)
		}
		return true, 0
	case "proof-observer":
		return true, runAppCLIParentDeathProof(false)
	case "proof-runner":
		return true, runAppCLIParentDeathProof(true)
	default:
		return false, 0
	}
}

func appCLILifetimeFailure(err error) (bool, int) {
	fmt.Fprintln(os.Stderr, "App CLI lifetime fixture:", err)
	return true, 2
}

func appCLILifetimeEnv(mode string, observer bool, executable, ready string) []string {
	var env []string
	for _, entry := range os.Environ() {
		keep := true
		for _, key := range []string{appCLIObserverEnv, appCLIRealBinaryEnv, appCLILifetimeModeEnv, appCLILifetimeReadyEnv} {
			if strings.HasPrefix(entry, key+"=") {
				keep = false
				break
			}
		}
		if keep {
			env = append(env, entry)
		}
	}
	env = append(env, appCLILifetimeModeEnv+"="+mode, appCLIRealBinaryEnv+"="+executable, appCLILifetimeReadyEnv+"="+ready)
	if observer {
		env = append(env, appCLIObserverEnv+"=capture-apply")
	}
	return env
}

func readAppCLIProcess(pid int) (appCLIOwnedProcess, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return appCLIOwnedProcess{}, err
	}
	// comm can contain spaces and parentheses; the final ')' ends that field.
	end := strings.LastIndex(string(data), ") ")
	if end < 0 {
		return appCLIOwnedProcess{}, fmt.Errorf("invalid owned process stat")
	}
	fields := strings.Fields(string(data[end+2:]))
	if len(fields) < 20 {
		return appCLIOwnedProcess{}, fmt.Errorf("incomplete owned process stat")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return appCLIOwnedProcess{}, fmt.Errorf("read owned parent PID: %w", err)
	}
	return appCLIOwnedProcess{PID: pid, ParentPID: parent, State: fields[0], StartTick: fields[19]}, nil
}

// A separate proof driver owns subreaper state, so package tests and their
// other subprocesses never acquire a process-global cleanup policy.
func runAppCLIParentDeathProof(throughRunner bool) (code int) {
	const prSetChildSubreaper = 0x24 // Linux PR_SET_CHILD_SUBREAPER, no capability required
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0); errno != 0 {
		fmt.Fprintln(os.Stderr, "enable owned lifetime proof subreaper:", errno)
		return 2
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mode := "block"
	if throughRunner {
		mode = "runner"
	}
	cmd := exec.CommandContext(ctx, executable, "app-lifetime-block")
	cmd.Env = appCLILifetimeEnv(mode, !throughRunner, executable, os.Getenv(appCLILifetimeReadyEnv))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	configureAppCLIProcess(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	joined := false
	defer func() {
		if !joined {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		// All orphaned fixture descendants are adopted by this proof driver.
		// Even a failing negative control is killed and reaped before return.
		if err := reapAppCLIFixtureChildren(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 2
		}
	}()
	var ready appCLILifetimeReady
	if err := pollAppCLILifetime(ctx, 5*time.Second, func() (bool, error) {
		data, err := os.ReadFile(os.Getenv(appCLILifetimeReadyEnv))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(data, &ready); err != nil {
			return false, nil //nolint:nilerr // a readiness write may still be in progress
		}
		return true, nil
	}); err != nil {
		fmt.Fprintln(os.Stderr, "wait for owned blocking child:", err)
		return 2
	}
	if ready.Child.ParentPID != ready.Observer.PID || (!throughRunner && ready.Observer.PID != cmd.Process.Pid) || (throughRunner && ready.Observer.ParentPID != cmd.Process.Pid) {
		fmt.Fprintln(os.Stderr, "lifetime fixture did not create the requested owned process chain")
		return 2
	}
	for _, process := range []appCLIOwnedProcess{ready.Observer, ready.Child} {
		current, err := readAppCLIProcess(process.PID)
		if err != nil || current.StartTick != process.StartTick || current.State == "Z" {
			fmt.Fprintln(os.Stderr, "owned child was not alive before abrupt parent death")
			return 2
		}
	}
	if err := cmd.Process.Kill(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	err = cmd.Wait()
	joined = true
	if err == nil {
		fmt.Fprintln(os.Stderr, "abruptly killed proof parent exited successfully")
		return 2
	}
	if err := pollAppCLILifetime(ctx, 3*time.Second, func() (bool, error) {
		for _, process := range []appCLIOwnedProcess{ready.Observer, ready.Child} {
			current, err := readAppCLIProcess(process.PID)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return false, err
			}
			if current.StartTick == process.StartTick && current.State != "Z" {
				return false, nil
			}
		}
		return true, nil
	}); err != nil {
		fmt.Fprintln(os.Stderr, "owned CLI survived abrupt parent death:", err)
		return 2
	}
	return 0
}

func pollAppCLILifetime(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if done, err := check(); done || err != nil {
			return err
		}
		select {
		case <-bounded.Done():
			return bounded.Err()
		case <-ticker.C:
		}
	}
}

func reapAppCLIFixtureChildren() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	forced := false
	err := pollAppCLILifetime(ctx, 3*time.Second, func() (bool, error) {
		for {
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if errors.Is(err, syscall.ECHILD) {
				return true, nil
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil {
				return false, err
			}
			if pid == 0 {
				break
			}
		}
		files, err := filepath.Glob("/proc/self/task/*/children")
		if err != nil {
			return false, err
		}
		for _, file := range files {
			data, err := os.ReadFile(file)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return false, err
			}
			for _, value := range strings.Fields(string(data)) {
				pid, err := strconv.Atoi(value)
				if err != nil {
					return false, err
				}
				process, err := readAppCLIProcess(pid)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return false, err
				}
				if process.ParentPID != os.Getpid() || process.State == "Z" {
					continue
				}
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					return false, err
				}
				forced = true
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("reap owned lifetime fixture children: %w", err)
	}
	if forced {
		return fmt.Errorf("lifetime proof required forced descendant cleanup")
	}
	return nil
}

func TestAppCLIObserverLinuxParentDeath(t *testing.T) {
	for _, mode := range []string{"proof-observer", "proof-runner"} {
		t.Run(mode, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable)
			cmd.Env = appCLILifetimeEnv(mode, false, executable, filepath.Join(t.TempDir(), "ready.json"))
			configureAppCLIProcess(cmd)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Linux abrupt-parent-death control failed: %v\n%s", err, output)
			}
		})
	}
}
