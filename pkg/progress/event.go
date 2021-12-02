package progress

import "time"

// EventStatus indicates the status of an action
type EventStatus int

const (
	// Working means that the current task is working
	Working EventStatus = iota
	// Done means that the current task is done
	Done
	// Error means that the current task has errored
	Error
)

// Event represents a progress event.
type Event struct {
	ID         string
	ParentID   string
	Text       string
	Status     EventStatus
	StatusText string

	startTime time.Time
	endTime   time.Time
	spinner   *spinner
}

// NewEvent new event
func NewEvent(ID string, status EventStatus, statusText string) Event {
	return Event{
		ID:         ID,
		Status:     status,
		StatusText: statusText,
	}
}

func (e EventStatus) String() string {
	switch e {
	case Done:
		return "Success!"
	case Working:
		return "Running"
	case Error:
		return "Error!"
	}
	return ""
}

func (e *Event) stop() {
	e.endTime = time.Now()
	e.spinner.Stop(e.Status)
}
