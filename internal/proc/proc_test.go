package proc

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLineRingKeepsMostRecentLines(t *testing.T) {
	t.Parallel()
	r := newLineRing(3)
	for _, l := range []string{"a", "b", "c", "d", "e"} {
		r.Add(l)
	}
	got := r.String()
	want := "c\nd\ne"
	if got != want {
		t.Fatalf("lineRing.String() = %q, want %q", got, want)
	}
}

func TestLineRingPartialFill(t *testing.T) {
	t.Parallel()
	r := newLineRing(5)
	r.Add("only")
	if got := r.String(); got != "only" {
		t.Fatalf("lineRing.String() = %q, want %q", got, "only")
	}
}

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	t.Parallel()
	res, err := Run(t.Context(), helperSpec("emitter", helperCmd))
	if err == nil {
		t.Fatal("Run() error = nil, want failure for non-zero exit")
	}
	if res.ExitCode == 0 {
		t.Fatalf("ExitCode = 0, want non-zero")
	}
	if !strings.Contains(res.Stdout, "hello stdout") {
		t.Errorf("Stdout = %q, want it to contain %q", res.Stdout, "hello stdout")
	}
	if !strings.Contains(res.Stderr, "hello stderr") {
		t.Errorf("Stderr = %q, want it to contain %q", res.Stderr, "hello stderr")
	}
}

func TestRunMissingExecutable(t *testing.T) {
	t.Parallel()
	_, err := Run(t.Context(), Spec{Path: "/definitely/not/here/tool"})
	if err == nil {
		t.Fatal("Run() error = nil, want not-found error")
	}
}

func TestPauseResumeRoundTrip(t *testing.T) {
	t.Parallel()
	p, err := Start(helperSpec("sleeper", helperSleep))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = p.Kill() }()

	go func() { _ = p.Consume(nil, nil) }()

	if err := p.Pause(); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if err := p.Resume(); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}
	if !p.WaitTimeout(10 * time.Second) {
		t.Fatal("process did not exit after Kill()")
	}
}

func TestPipeWiresTwoProcesses(t *testing.T) {
	t.Parallel()
	// The zero-shell pipeline is the core of the new design, so it gets an
	// explicit test: producer | consumer, wired with an in-process pipe.
	producer, err := Start(helperSpec("producer", helperCmd))
	if err != nil {
		t.Fatalf("Start(producer) error = %v", err)
	}
	consumer, err := Start(helperSpec("consumer", helperSink))
	if err != nil {
		_ = producer.Kill()
		t.Fatalf("Start(consumer) error = %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Pipe the producer's stdout into the consumer's stdin. This is the
		// zero-shell replacement for `vspipe ... | x265 ...`.
		_, _ = io.Copy(consumer.Stdin(), producer.Stdout())
		_ = consumer.Stdin().Close()
	}()

	consumerSeen := make(chan string, 1)
	go func() {
		_ = consumer.Finish(nil, nil)
		consumerSeen <- consumer.RecentOutput()
	}()
	// The producer's stdout is owned by the pipe above, so only stderr is read
	// here -- exactly how a real pipeline producer is consumed.
	_ = producer.FinishStderr(nil)

	// The io.Copy must complete before the producer is reaped, otherwise the
	// tail of its stdout can be lost.
	wg.Wait()
	got := <-consumerSeen

	if !strings.Contains(got, "hello stdout") {
		t.Fatalf("consumer saw %q, want it to contain the producer's output", got)
	}
}

func TestKillIsIdempotent(t *testing.T) {
	t.Parallel()
	p, err := Start(helperSpec("sleeper", helperSleep))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	go func() { _ = p.Consume(nil, nil) }()
	if err := p.Kill(); err != nil {
		t.Fatalf("first Kill() error = %v", err)
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("second Kill() error = %v", err)
	}
}

func TestExitCodeAfterWait(t *testing.T) {
	t.Parallel()
	p, err := Start(helperSpec("exiter", helperExit, "3"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	go func() { _ = p.Consume(nil, nil) }()
	if !p.WaitTimeout(10 * time.Second) {
		t.Fatal("process did not exit")
	}
	if got := p.ExitCode(); got != 3 {
		t.Fatalf("ExitCode() = %d, want 3", got)
	}
}

func TestWrapKeepsUnwrapChain(t *testing.T) {
	t.Parallel()
	err := errors.New("boom")
	if !errors.Is(wrapForTest(err), err) {
		t.Fatal("wrapped error does not unwrap to its cause")
	}
}
