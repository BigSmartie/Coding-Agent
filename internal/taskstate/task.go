package taskstate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Details   string    `json:"details,omitempty"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updatedAt"`
}

var taskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func Validate(task Task) error {
	if !taskID.MatchString(task.ID) {
		return fmt.Errorf("task id must be 1-64 ASCII letters, digits, underscores, or hyphens")
	}
	if strings.TrimSpace(task.Title) == "" || len(task.Title) > 200 || len(task.Details) > 2000 {
		return fmt.Errorf("task title/details exceed limits")
	}
	switch task.Status {
	case "pending", "in_progress", "completed", "canceled":
	default:
		return fmt.Errorf("invalid task status")
	}
	return nil
}

func Upsert(tasks []Task, task Task) ([]Task, error) {
	if err := Validate(task); err != nil {
		return nil, err
	}
	out := append([]Task(nil), tasks...)
	for i := range out {
		if out[i].ID == task.ID {
			out[i] = task
			return out, nil
		}
	}
	if len(out) >= 32 {
		return nil, fmt.Errorf("session task limit reached")
	}
	out = append(out, task)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
