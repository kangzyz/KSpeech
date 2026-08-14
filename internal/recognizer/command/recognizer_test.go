package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kangzyz/KSpeech/internal/plugin"
)

const helperSeparator = "--kspeech-command-helper"

func TestCommandHelperProcess(t *testing.T) {
	scenario := ""
	var scenarioArguments []string
	for index, argument := range os.Args {
		if argument == helperSeparator && index+1 < len(os.Args) {
			scenario = os.Args[index+1]
			scenarioArguments = os.Args[index+2:]
			break
		}
	}
	if scenario == "" {
		return
	}

	switch scenario {
	case "protocol":
		_, _ = fmt.Fprint(os.Stdout, "临时\r\n\r\n")
		_, _ = fmt.Fprint(os.Stderr, "诊断日志\n")
		_ = os.Stdout.Sync()
		_ = os.Stderr.Sync()
		os.Exit(7)
	case "hang":
		_, _ = fmt.Fprint(os.Stdout, "运行中\n")
		_ = os.Stdout.Sync()
		for {
			time.Sleep(time.Second)
		}
	case "stderr-flood":
		_, _ = fmt.Fprint(os.Stderr, strings.Repeat("错", 400_000))
		_, _ = fmt.Fprint(os.Stdout, "未阻塞\n")
		_ = os.Stdout.Sync()
		_ = os.Stderr.Sync()
		os.Exit(0)
	case "spawn-child-exit":
		marker := decodeHelperPath(scenarioArguments)
		child := exec.Command(
			os.Args[0],
			"-test.run=^TestCommandHelperProcess$",
			"--",
			helperSeparator,
			"delayed-marker",
			scenarioArguments[0],
		)
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "start descendant: %v\n", err)
			os.Exit(65)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(marker + ".ready"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				_, _ = fmt.Fprintln(os.Stderr, "descendant did not become ready")
				os.Exit(66)
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = fmt.Fprintf(os.Stdout, "descendant:%d\n", child.Process.Pid)
		_ = os.Stdout.Sync()
		os.Exit(0)
	case "delayed-marker":
		marker := decodeHelperPath(scenarioArguments)
		if err := os.WriteFile(marker+".ready", []byte("ready"), 0o600); err != nil {
			os.Exit(67)
		}
		time.Sleep(750 * time.Millisecond)
		if err := os.WriteFile(marker, []byte("survived"), 0o600); err != nil {
			os.Exit(68)
		}
		os.Exit(0)
	default:
		os.Exit(64)
	}
}

func decodeHelperPath(arguments []string) string {
	if len(arguments) != 1 {
		os.Exit(69)
	}
	path, err := base64.RawURLEncoding.DecodeString(arguments[0])
	if err != nil {
		os.Exit(70)
	}
	return string(path)
}

func newTestRecognizer(t *testing.T, scenario, logFile string, extraArguments ...string) *Recognizer {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	recognizer := New(plugin.Metadata{ID: "from-caller", Name: "test command"})
	config := Config{
		Command: executable,
		Arguments: strings.Join(append(
			[]string{"-test.run=^TestCommandHelperProcess$", "--", helperSeparator, scenario},
			extraArguments...,
		), " "),
		LogFile: logFile,
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := recognizer.LoadConfig(data); err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	return recognizer
}

func TestRecognizerKillsDescendantsAfterUnexpectedParentExit(t *testing.T) {
	marker := t.TempDir() + string(os.PathSeparator) + "descendant-survived"
	encodedMarker := base64.RawURLEncoding.EncodeToString([]byte(marker))
	recognizer := newTestRecognizer(t, "spawn-child-exit", "", encodedMarker)
	partials := make(chan plugin.Text, 1)
	errorsSeen := make(chan error, 1)
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{
		Partial: func(text plugin.Text) { partials <- text },
		Error:   func(err error) { errorsSeen <- err },
	})

	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := receive(t, partials, "descendant start result"); !strings.HasPrefix(got.Text, "descendant:") {
		t.Fatalf("partial text = %q, want descendant PID", got.Text)
	}
	if err := receive(t, errorsSeen, "unexpected parent-exit error"); !errors.Is(err, ErrUnexpectedExit) {
		t.Fatalf("error = %v, want ErrUnexpectedExit", err)
	}
	if _, err := os.Stat(marker + ".ready"); err != nil {
		t.Fatalf("descendant readiness marker: %v", err)
	}
	time.Sleep(time.Second)
	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("descendant survived process-guard cleanup and wrote %q", data)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read descendant marker: %v", err)
	}
}

func receive[T any](t *testing.T, channel <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(10 * time.Second):
		var zero T
		t.Fatalf("timed out waiting for %s", description)
		return zero
	}
}

func TestRecognizerEmitsProtocolAndUnexpectedExit(t *testing.T) {
	recognizer := newTestRecognizer(t, "protocol", "")
	partials := make(chan plugin.Text, 2)
	finals := make(chan plugin.Text, 2)
	errorsSeen := make(chan error, 2)
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{
		Partial: func(text plugin.Text) { partials <- text },
		Final:   func(text plugin.Text) { finals <- text },
		Error:   func(err error) { errorsSeen <- err },
	})

	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := receive(t, partials, "partial result"); got.Text != "临时" || got.Time.IsZero() {
		t.Fatalf("partial = %#v", got)
	}
	if got := receive(t, finals, "final result"); got.Text != "临时" || got.Time.IsZero() {
		t.Fatalf("final = %#v", got)
	}
	exitErr := receive(t, errorsSeen, "unexpected-exit error")
	if !errors.Is(exitErr, ErrUnexpectedExit) {
		t.Fatalf("error = %v, want ErrUnexpectedExit", exitErr)
	}
	var unexpected *UnexpectedExitError
	if !errors.As(exitErr, &unexpected) || unexpected.Cause == nil {
		t.Fatalf("error = %#v, want UnexpectedExitError with exit cause", exitErr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for recognizer.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if recognizer.Running() {
		t.Fatal("recognizer remained running after process exit")
	}
}

func TestRecognizerAlwaysDrainsStderr(t *testing.T) {
	recognizer := newTestRecognizer(t, "stderr-flood", "")
	partials := make(chan plugin.Text, 1)
	errorsSeen := make(chan error, 1)
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{
		Partial: func(text plugin.Text) { partials <- text },
		Error:   func(err error) { errorsSeen <- err },
	})
	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, partials, "stdout after large stderr"); got.Text != "未阻塞" {
		t.Fatalf("partial text = %q, want 未阻塞", got.Text)
	}
	if err := receive(t, errorsSeen, "unexpected-exit error"); !errors.Is(err, ErrUnexpectedExit) {
		t.Fatalf("error = %v, want ErrUnexpectedExit", err)
	}
}

func TestRecognizerLogOpenFailureReportsAndContinuesDrainingStderr(t *testing.T) {
	// Opening an existing directory as a file fails reliably on every target.
	// The recognizer must still start and consume the helper's large stderr.
	recognizer := newTestRecognizer(t, "stderr-flood", t.TempDir())
	partials := make(chan plugin.Text, 1)
	warningsSeen := make(chan error, 1)
	errorsSeen := make(chan error, 1)
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{
		Partial: func(text plugin.Text) { partials <- text },
		Warning: func(err error) { warningsSeen <- err },
		Error:   func(err error) { errorsSeen <- err },
	})

	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v; log failure must not block the command", err)
	}
	logErr := receive(t, warningsSeen, "log-open warning")
	if !strings.Contains(logErr.Error(), "open command recognizer stderr log") {
		t.Fatalf("first error = %v, want log-open failure", logErr)
	}
	if got := receive(t, partials, "stdout after failed log and large stderr"); got.Text != "未阻塞" {
		t.Fatalf("partial text = %q, want 未阻塞", got.Text)
	}
	if err := receive(t, errorsSeen, "unexpected-exit error"); !errors.Is(err, ErrUnexpectedExit) {
		t.Fatalf("second error = %v, want ErrUnexpectedExit", err)
	}
}

func TestRecognizerAppendsStderrLog(t *testing.T) {
	logFile := t.TempDir() + string(os.PathSeparator) + "stderr.log"
	if err := os.WriteFile(logFile, []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recognizer := newTestRecognizer(t, "protocol", logFile)
	errorsSeen := make(chan error, 1)
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{Error: func(err error) { errorsSeen <- err }})
	if err := recognizer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = receive(t, errorsSeen, "unexpected-exit error")
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "existing\n诊断日志\n" {
		t.Fatalf("stderr log = %q", got)
	}
}

func TestRecognizerStopIsSilentAndRestartable(t *testing.T) {
	recognizer := newTestRecognizer(t, "hang", "")
	partials := make(chan plugin.Text, 2)
	errorsSeen := make(chan error, 2)
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{
		Partial: func(text plugin.Text) { partials <- text },
		Error:   func(err error) { errorsSeen <- err },
	})

	for run := 1; run <= 2; run++ {
		if err := recognizer.Start(context.Background()); err != nil {
			t.Fatalf("Start() run %d error = %v", run, err)
		}
		if err := recognizer.Start(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("second Start() error = %v, want ErrAlreadyRunning", err)
		}
		if err := recognizer.LoadConfig([]byte(`{"Command":"changed"}`)); !errors.Is(err, ErrConfigWhileRunning) {
			t.Fatalf("LoadConfig() while running error = %v, want ErrConfigWhileRunning", err)
		}
		if got := receive(t, partials, "running partial"); got.Text != "运行中" {
			t.Fatalf("partial text = %q", got.Text)
		}
		if err := recognizer.Stop(); err != nil {
			t.Fatalf("Stop() run %d error = %v", run, err)
		}
		if recognizer.Running() {
			t.Fatalf("Running() after Stop() run %d = true", run)
		}
		select {
		case err := <-errorsSeen:
			t.Fatalf("user-initiated Stop() reported error: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
	}
	if err := recognizer.Stop(); err != nil {
		t.Fatalf("idempotent Stop() error = %v", err)
	}
}

func TestRecognizerContextCancellationIsSilent(t *testing.T) {
	recognizer := newTestRecognizer(t, "hang", "")
	partials := make(chan plugin.Text, 1)
	errorsSeen := make(chan error, 1)
	recognizer.SetCallbacks(plugin.RecognizerCallbacks{
		Partial: func(text plugin.Text) { partials <- text },
		Error:   func(err error) { errorsSeen <- err },
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := recognizer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	_ = receive(t, partials, "running partial")
	cancel()
	deadline := time.Now().Add(10 * time.Second)
	for recognizer.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if recognizer.Running() {
		t.Fatal("context cancellation did not stop recognizer")
	}
	select {
	case err := <-errorsSeen:
		t.Fatalf("context cancellation reported abnormal exit: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}
