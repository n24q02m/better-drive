package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/engine"
	"github.com/n24q02m/better-drive/internal/exitcode"
)

type fakeMountEngine struct {
	configured     bool
	configuredErr  error
	configuredName string
	mountContext   context.Context
	mountParams    engine.MountParams
	mountErr       error
	closed         bool
}

func (f *fakeMountEngine) RemoteConfigured(name string) (bool, error) {
	f.configuredName = name
	return f.configured, f.configuredErr
}

func (f *fakeMountEngine) Mount(ctx context.Context, p engine.MountParams) error {
	f.mountContext = ctx
	f.mountParams = p
	return f.mountErr
}

func (f *fakeMountEngine) Close() { f.closed = true }

func mountFixture(t *testing.T, body string, service *fakeMountEngine) (*bytes.Buffer, *bytes.Buffer, *string, *int) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", configPath)
	var factoryConfig string
	factoryCalls := 0
	cmd := mountCmdWithFactory(func(path string) mountEngine {
		factoryCalls++
		factoryConfig = path
		return service
	})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	return &out, &errOut, &factoryConfig, &factoryCalls
}

func TestMountCmdPassesRemoteMountpointReadOnlyAndStreams(t *testing.T) {
	service := &fakeMountEngine{configured: true}
	out, errOut, factoryConfig, factoryCalls := mountFixture(t, `rclone_config = "X:/custom-rclone.conf"`, service)
	cmd := mountCmdWithFactory(func(path string) mountEngine {
		*factoryCalls++
		*factoryConfig = path
		return service
	})
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs([]string{"gdrive:Documents", "G:", "--read-only"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if *factoryCalls != 1 || *factoryConfig != "X:/custom-rclone.conf" {
		t.Fatalf("factory calls/config = %d, %q", *factoryCalls, *factoryConfig)
	}
	if service.configuredName != "gdrive" {
		t.Fatalf("RemoteConfigured name = %q, want gdrive", service.configuredName)
	}
	if service.mountParams.Remote != "gdrive:Documents" || service.mountParams.Mountpoint != "G:" || !service.mountParams.ReadOnly {
		t.Fatalf("Mount params = %+v", service.mountParams)
	}
	if service.mountParams.Stdout != out || service.mountParams.Stderr != errOut {
		t.Fatal("mount did not wire command stdout/stderr to the engine")
	}
	if service.mountContext == nil || service.closed == false {
		t.Fatal("mount must pass a context and close the engine")
	}
}

func TestMountCmdAllowsConfigWithoutPairs(t *testing.T) {
	service := &fakeMountEngine{configured: true}
	_, _, _, _ = mountFixture(t, `rclone_config = "X:/custom-rclone.conf"`, service)
	cmd := mountCmdWithFactory(func(string) mountEngine { return service })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"gdrive:", "*"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mount with no [[pair]] config: %v", err)
	}
	if service.mountParams.Mountpoint != "*" {
		t.Fatalf("mountpoint = %q, want passthrough *", service.mountParams.Mountpoint)
	}
}

func TestMountCmdAllowsMissingConfig(t *testing.T) {
	t.Setenv("BETTER_DRIVE_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
	service := &fakeMountEngine{configured: true}
	called := false
	cmd := mountCmdWithFactory(func(string) mountEngine {
		called = true
		return service
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"gdrive:", "G:"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mount with missing config: %v", err)
	}
	if !called {
		t.Fatal("missing better-drive config must fall back to rclone discovery")
	}
}

func TestMountCmdRejectsMalformedExistingConfigBeforeEngine(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(`rclone_config = "unterminated`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BETTER_DRIVE_CONFIG", configPath)
	called := false
	cmd := mountCmdWithFactory(func(string) mountEngine {
		called = true
		return &fakeMountEngine{}
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"gdrive:", "G:"})
	err := cmd.Execute()
	if err == nil || exitcode.Code(err) != exitcode.ConfigErrorCode {
		t.Fatalf("error = %v, code = %d, want config error", err, exitcode.Code(err))
	}
	if called {
		t.Fatal("engine factory called before malformed config was rejected")
	}
	if hint := exitcode.RemediationOf(err); !strings.Contains(hint, configPath) {
		t.Fatalf("remediation = %q, want config path", hint)
	}
}

func TestMountCmdValidatesArityAndRemotePath(t *testing.T) {
	service := &fakeMountEngine{configured: true}
	for _, args := range [][]string{
		{},
		{"gdrive:"},
		{"gdrive:", "G:", "extra"},
		{"not-a-remote", "G:"},
		{":path", "G:"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			cmd := mountCmdWithFactory(func(string) mountEngine { return service })
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatalf("args %q: want validation error", args)
			}
		})
	}
}

func TestMountCmdRequiresConfiguredOAuthRemote(t *testing.T) {
	service := &fakeMountEngine{configured: false}
	_, _, _, _ = mountFixture(t, "", service)
	cmd := mountCmdWithFactory(func(string) mountEngine { return service })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"work:Documents", "G:"})
	err := cmd.Execute()
	if err == nil || exitcode.Code(err) != exitcode.RemoteNotConfigured {
		t.Fatalf("error = %v, code = %d, want remote-not-configured", err, exitcode.Code(err))
	}
	if hint := exitcode.RemediationOf(err); hint != "run: better-drive setup --remote work" {
		t.Fatalf("remediation = %q", hint)
	}
}

func TestMountCmdTreatsInterruptAsCleanUnmount(t *testing.T) {
	service := &fakeMountEngine{configured: true, mountErr: context.Canceled}
	_, _, _, _ = mountFixture(t, "", service)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := mountCmdWithFactory(func(string) mountEngine { return service })
	cmd.SetContext(ctx)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"gdrive:", "G:"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected canceled foreground mount to unmount cleanly, got %v", err)
	}
}

func TestMountDriverErrorRemediationByOS(t *testing.T) {
	tests := []struct {
		goos, message, want string
	}{
		{"windows", "failed to mount: WinFsp not found", "scoop install winfsp"},
		{"linux", "fusermount3: command not found", "FUSE"},
		{"linux", "failed to open /dev/fuse", "/dev/fuse"},
		{"darwin", "mount failed: macFUSE unavailable", "rclone mount backend"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+tt.message, func(t *testing.T) {
			base := errors.New(tt.message)
			err := mountDriverError(tt.goos, base)
			if !errors.Is(err, base) {
				t.Fatal("driver mapping must preserve the original mount error")
			}
			if hint := exitcode.RemediationOf(err); !strings.Contains(hint, tt.want) {
				t.Fatalf("remediation = %q, want %q", hint, tt.want)
			}
		})
	}
	base := errors.New("remote permission denied")
	if got := mountDriverError("windows", base); got != base {
		t.Fatalf("ordinary mount error was reclassified: %v", got)
	}
}

func TestMountHelpDocumentsContractAndDriverRequirements(t *testing.T) {
	cmd := mountCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("mount --help: %v", err)
	}
	help := out.String()
	for _, want := range []string{"mount <remote:path> <mountpoint>", "--read-only", "Ctrl+C", "WinFsp", "FUSE"} {
		if !strings.Contains(help, want) {
			t.Errorf("mount help missing %q:\n%s", want, help)
		}
	}
}
