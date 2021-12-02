package progress

import (
	"runtime"
	"time"
)

type spinner struct {
	time   time.Time
	index  int
	chars  []string
	stop   bool
	done   string
	err    string
	status EventStatus
}

func newSpinner() *spinner {
	chars := []string{
		"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
	}
	done := "✔"
	err := "✘"

	if runtime.GOOS == "windows" {
		chars = []string{"-"}
		done = "-"
		err = "-"
	}

	return &spinner{
		index: 0,
		time:  time.Now(),
		chars: chars,
		done:  done,
		err:   err,
	}
}

func (s *spinner) String() string {
	if s.stop {
		return s.finalState()
	}

	d := time.Since(s.time)
	if d.Milliseconds() > 100 {
		s.index = (s.index + 1) % len(s.chars)
	}

	return s.chars[s.index]
}

func (s *spinner) Stop(status EventStatus) {
	s.stop = true
	s.status = status
}

func (s *spinner) finalState() string {
	switch s.status {
	case Done:
		return s.done
	case Error:
		return s.err
	default:
		return s.chars[s.index]
	}
}
