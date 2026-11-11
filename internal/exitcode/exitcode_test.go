package exitcode

import (
	"errors"
	"fmt"
	"testing"
)

type usage struct{}

func (usage) Error() string { return "bad usage" }
func (usage) ExitCode() int { return Usage }

func TestClassifyNilIsOK(t *testing.T) {
	if got := Classify(nil); got != OK {
		t.Fatalf("got %d", got)
	}
}

func TestClassifyWrappedCoded(t *testing.T) {
	if got := Classify(fmt.Errorf("x: %w", usage{})); got != Usage {
		t.Fatalf("got %d, want %d", got, Usage)
	}
}

func TestClassifyPlainErrorIsUnexpected(t *testing.T) {
	if got := Classify(errors.New("boom")); got != Unexpected {
		t.Fatalf("got %d, want %d", got, Unexpected)
	}
}
