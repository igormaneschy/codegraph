//go:build windows

package scip

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestAssignBeforeResumeOrdersContainmentBeforeExecution(t *testing.T) {
	var events []string
	err := assignBeforeResume(
		func() error {
			events = append(events, "assign")
			return nil
		},
		func() error {
			events = append(events, "resume")
			return nil
		},
		func() error {
			events = append(events, "abort")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("assignBeforeResume: %v", err)
	}
	if want := []string{"assign", "resume"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}

func TestAssignBeforeResumeFailsClosedOnAttachOrResumeFailure(t *testing.T) {
	tests := []struct {
		name       string
		assignErr  error
		resumeErr  error
		wantEvents []string
	}{
		{
			name:       "attach failure",
			assignErr:  errors.New("attach failed"),
			wantEvents: []string{"assign", "abort"},
		},
		{
			name:       "resume failure",
			resumeErr:  errors.New("resume failed"),
			wantEvents: []string{"assign", "resume", "abort"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []string
			err := assignBeforeResume(
				func() error {
					events = append(events, "assign")
					return tt.assignErr
				},
				func() error {
					events = append(events, "resume")
					return tt.resumeErr
				},
				func() error {
					events = append(events, "abort")
					return nil
				},
			)
			if err == nil {
				t.Fatal("assignBeforeResume returned nil after lifecycle failure")
			}
			if !reflect.DeepEqual(events, tt.wantEvents) {
				t.Fatalf("lifecycle events = %v, want %v", events, tt.wantEvents)
			}
		})
	}
}

func TestWindowsOwnedHandleClosesExactlyOnce(t *testing.T) {
	var closeCount int
	closeErr := errors.New("close failed")
	handle := &windowsOwnedHandle{
		handle: windows.Handle(1),
		closeFn: func(windows.Handle) error {
			closeCount++
			return closeErr
		},
	}
	if err := handle.close(); !errors.Is(err, closeErr) {
		t.Fatalf("first close error = %v, want %v", err, closeErr)
	}
	if err := handle.close(); !errors.Is(err, closeErr) {
		t.Fatalf("second close error = %v, want %v", err, closeErr)
	}
	if closeCount != 1 {
		t.Fatalf("CloseHandle calls = %d, want exactly one", closeCount)
	}
}

func TestContextCancellationIsObservedByStartupSeam(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &processTree{ctx: ctx}
	if err := p.contextErrorOr("fallback"); !errors.Is(err, context.Canceled) {
		t.Fatalf("contextErrorOr = %v, want context.Canceled", err)
	}
}

func TestWindowsEnvironmentBlockHasSortedDoubleTerminator(t *testing.T) {
	empty, err := windowsEnvironmentBlock(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(empty, []uint16{0, 0}) {
		t.Fatalf("empty environment block = %v, want double terminator", empty)
	}

	block, err := windowsEnvironmentBlock([]string{"Z=last", "a=first"})
	if err != nil {
		t.Fatal(err)
	}
	if len(block) < 2 || block[len(block)-1] != 0 || block[len(block)-2] != 0 {
		t.Fatalf("environment block lacks double terminator: %v", block)
	}
	if block[0] != 'a' || block[1] != 0 {
		t.Fatalf("environment block first key starts with UTF-16 %v, want a", block[:2])
	}
	if _, err := windowsEnvironmentBlock([]string{fmt.Sprintf("bad\x00=%d", 1)}); err == nil {
		t.Fatal("environment block accepted an embedded NUL")
	}
}
