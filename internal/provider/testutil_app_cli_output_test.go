package provider

// These acceptance tests must inspect the real CLI's rendered warnings and exit
// status. Observing its output catches diagnostics that provider-only tests miss.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const appCLIObserverEnv = "KUPE_APP_TEST_CLI_OBSERVER"
const appCLIRealBinaryEnv = "KUPE_APP_TEST_REAL_CLI"
const appCLIOutputDirEnv = "KUPE_APP_TEST_CLI_OUTPUT_DIR"

// The test executable can observe the real CLI's apply output without a shell,
// another provider process or a second HTTP stack. Normal tests run unchanged.
func TestMain(m *testing.M) {
	if os.Getenv(appCLIObserverEnv) == "capture-apply" {
		os.Exit(runAppCLIObserver())
	}
	if handled, code := runAppCLIPlatformFixture(); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func runAppCLIObserver() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd := exec.CommandContext(ctx, os.Getenv(appCLIRealBinaryEnv), os.Args[1:]...) // #nosec G204 -- test-only, verified CLI selected by the acceptance runner
	configureAppCLIProcess(cmd)
	// Do not let the observer marker reach any process the real CLI starts.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, appCLIObserverEnv+"=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	var outputs []*os.File
	if len(os.Args) > 1 && os.Args[1] == "apply" {
		for _, stream := range []string{"stdout", "stderr"} {
			output, err := os.OpenFile(filepath.Join(os.Getenv(appCLIOutputDirEnv), stream+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				for _, opened := range outputs {
					_ = opened.Close()
				}
				fmt.Fprintln(os.Stderr, "cannot capture test CLI apply output:", err)
				return 2
			}
			outputs = append(outputs, output)
			if stream == "stdout" {
				cmd.Stdout = io.MultiWriter(os.Stdout, output)
			} else {
				cmd.Stderr = io.MultiWriter(os.Stderr, output)
			}
		}
	}
	err := cmd.Run()
	var closeErr error
	for _, output := range outputs {
		closeErr = errors.Join(closeErr, output.Close())
	}
	if closeErr != nil {
		fmt.Fprintln(os.Stderr, "cannot close test CLI apply output:", closeErr)
		return 2
	}
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode()
	}
	if ctx.Err() != nil {
		return 130
	}
	fmt.Fprintln(os.Stderr, "cannot run configured test CLI:", err)
	return 2
}

type appCLIOutputCapture struct{ directory string }

func captureAppCLIOutput(t *testing.T) appCLIOutputCapture {
	t.Helper()
	realBinary := os.Getenv("TF_ACC_TERRAFORM_PATH")
	if realBinary == "" {
		for _, name := range []string{"terraform", "tofu"} {
			if binary, err := exec.LookPath(name); err == nil {
				realBinary = binary
				break
			}
		}
	}
	resolved, err := exec.LookPath(realBinary)
	if err != nil {
		t.Fatalf("set TF_ACC_TERRAFORM_PATH to an installed Terraform or OpenTofu CLI: %v", err)
	}
	realBinary, err = filepath.Abs(resolved)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if realBinary == observer {
		t.Fatal("the real CLI must differ from the test output observer")
	}
	capture := appCLIOutputCapture{directory: t.TempDir()}
	t.Setenv(appCLIRealBinaryEnv, realBinary)
	t.Setenv(appCLIOutputDirEnv, capture.directory)
	t.Setenv(appCLIObserverEnv, "capture-apply")
	t.Setenv("TF_ACC_TERRAFORM_PATH", observer)
	return capture
}

func (capture appCLIOutputCapture) read() (string, error) {
	stdout, outErr := os.ReadFile(filepath.Join(capture.directory, "stdout.log"))
	stderr, errErr := os.ReadFile(filepath.Join(capture.directory, "stderr.log"))
	if err := errors.Join(outErr, errErr); err != nil {
		return "", fmt.Errorf("read captured CLI apply output: %w", err)
	}
	return string(stdout) + "\n" + string(stderr), nil
}
