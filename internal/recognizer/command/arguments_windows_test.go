//go:build windows

package command

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

const windowsArgumentProbe = "--kspeech-windows-argument-probe"

func TestWindowsArgumentProbe(t *testing.T) {
	if os.Getenv("KSPEECH_WINDOWS_ARGUMENT_PROBE") != "1" {
		return
	}
	for index, argument := range os.Args {
		if argument == windowsArgumentProbe {
			if err := json.NewEncoder(os.Stdout).Encode(os.Args[index+1:]); err != nil {
				os.Exit(71)
			}
			os.Exit(0)
		}
	}
	os.Exit(72)
}

func TestConfigureProcessPreservesRawWindowsArguments(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	rawArguments := `-test.run=^TestWindowsArgumentProbe$ -- ` + windowsArgumentProbe + ` "C:\model path\\" 'single quoted' ""`
	cmd := exec.Command(executable)
	if err := configureProcess(cmd, rawArguments); err != nil {
		t.Fatalf("configureProcess() error = %v", err)
	}

	wantCommandLine := syscall.EscapeArg(cmd.Path) + " " + rawArguments
	if got := cmd.SysProcAttr.CmdLine; got != wantCommandLine {
		t.Fatalf("CmdLine = %q, want exact legacy argument suffix %q", got, wantCommandLine)
	}
	if !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&createNewProcessGroup == 0 {
		t.Fatalf("SysProcAttr lost process isolation flags: %#v", cmd.SysProcAttr)
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != executable {
		t.Fatalf("Args = %#v; raw arguments must not be reparsed into exec.Cmd.Args", cmd.Args)
	}

	cmd.Env = append(os.Environ(), "KSPEECH_WINDOWS_ARGUMENT_PROBE=1")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("argument probe failed: %v", err)
	}
	var got []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &got); err != nil {
		t.Fatalf("decode argument probe %q: %v", output, err)
	}
	want := []string{`C:\model path\`, `'single`, `quoted'`, ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("child arguments = %#v, want %#v", got, want)
	}
}

func TestConfigureProcessRunsBatchFileThroughComSpec(t *testing.T) {
	commandInterpreter, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Fatalf("locate cmd.exe: %v", err)
	}
	t.Setenv("ComSpec", commandInterpreter)

	directory := filepath.Join(t.TempDir(), "directory & with spaces")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	batch := []byte("@echo off\r\n" +
		"echo first=[%~1]\r\n" +
		"echo second=[%~2]\r\n")
	resolvedInterpreter, err := exec.LookPath(commandInterpreter)
	if err != nil {
		t.Fatal(err)
	}

	for _, extension := range []string{".cmd", ".bat"} {
		t.Run(extension, func(t *testing.T) {
			batchPath := filepath.Join(directory, "argument probe"+extension)
			if err := os.WriteFile(batchPath, batch, 0o600); err != nil {
				t.Fatal(err)
			}

			rawArguments := `"value with spaces" plain`
			cmd := exec.Command(batchPath)
			if err := configureProcess(cmd, rawArguments); err != nil {
				t.Fatalf("configureProcess() error = %v", err)
			}
			wantBatchCommand := `"` + batchPath + `" ` + rawArguments
			wantCommandLine := syscall.EscapeArg(resolvedInterpreter) + ` /d /s /c "` + wantBatchCommand + `"`
			if got := cmd.SysProcAttr.CmdLine; got != wantCommandLine {
				t.Fatalf("CmdLine = %q, want %q", got, wantCommandLine)
			}
			if !strings.EqualFold(cmd.Path, resolvedInterpreter) {
				t.Fatalf("Path = %q, want ComSpec %q", cmd.Path, resolvedInterpreter)
			}
			if len(cmd.Args) != 1 || !strings.EqualFold(cmd.Args[0], resolvedInterpreter) {
				t.Fatalf("Args = %#v, want only ComSpec argv[0]", cmd.Args)
			}
			if !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&createNewProcessGroup == 0 {
				t.Fatalf("SysProcAttr lost process isolation flags: %#v", cmd.SysProcAttr)
			}

			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("batch argument probe failed: %v", err)
			}
			wantOutput := []byte("first=[value with spaces]\r\nsecond=[plain]\r\n")
			if !bytes.Equal(output, wantOutput) {
				t.Fatalf("batch output = %q, want %q", output, wantOutput)
			}
		})
	}
}

func TestConfigureProcessRunsBatchRelativeToWorkingDirectory(t *testing.T) {
	commandInterpreter, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Fatalf("locate cmd.exe: %v", err)
	}
	t.Setenv("ComSpec", commandInterpreter)

	directory := t.TempDir()
	const batchName = "working directory probe.cmd"
	if err := os.WriteFile(
		filepath.Join(directory, batchName),
		[]byte("@echo off\r\necho relative-ok\r\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(batchName)
	if err := configureProcess(cmd, ""); err != nil {
		t.Fatalf("configureProcess() error = %v", err)
	}
	if cmd.Err != nil {
		t.Fatalf("configureProcess() retained stale batch LookPath error: %v", cmd.Err)
	}
	// Without the explicit ".\" prefix cmd.exe only searches PATH, so the probe
	// fails wherever NoDefaultCurrentDirectoryInExePath is set.
	if want := `"` + `.\` + batchName + `"`; !strings.Contains(cmd.SysProcAttr.CmdLine, want) {
		t.Fatalf("CmdLine = %q, want a working-directory relative batch path %s", cmd.SysProcAttr.CmdLine, want)
	}
	cmd.Dir = directory
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("relative batch probe failed: %v", err)
	}
	if want := []byte("relative-ok\r\n"); !bytes.Equal(output, want) {
		t.Fatalf("batch output = %q, want %q", output, want)
	}
}
