package main

import (
	"bufio"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestGracefulShutdownSecondInterruptForcesExit(t *testing.T) {
	if os.Getenv("SINAVIDEO_SIGNAL_HELPER") == "1" {
		ctx, stop := gracefulShutdownContext(zap.NewNop())
		defer stop()
		println("ready")
		<-ctx.Done()
		println("graceful")
		select {}
	}

	command := exec.Command(os.Args[0], "-test.run=^TestGracefulShutdownSecondInterruptForcesExit$")
	command.Env = append(os.Environ(), "SINAVIDEO_SIGNAL_HELPER=1")
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})

	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	waitLine(t, lines, "ready")
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitLine(t, lines, "graceful")
	if err := command.Process.Signal(os.Signal(syscall.Signal(0))); err != nil {
		t.Fatalf("process exited after first interrupt: %v", err)
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("process survived second interrupt")
	}
}

func waitLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	select {
	case got := <-lines:
		if got != want {
			t.Fatalf("line = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}
