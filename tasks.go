package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// ──────────────────────────────────────────────────────────────
//  Task system — Claude Code-style todo list for agent progress
// ──────────────────────────────────────────────────────────────

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskCompleted  TaskStatus = "completed"
)

// TaskItem represents a single task in the agent's work plan.
type TaskItem struct {
	ID          int        `json:"id"`
	Subject     string     `json:"subject"`      // imperative: "Fix auth bug"
	Description string     `json:"description"`   // detailed requirements
	ActiveForm  string     `json:"active_form"`   // present continuous: "Fixing auth bug"
	Status      TaskStatus `json:"status"`
	BlockedBy   []int      `json:"blocked_by,omitempty"` // IDs that must complete first
}

// TaskStore holds all tasks for the current agent session.
type TaskStore struct {
	mu    sync.RWMutex
	tasks []*TaskItem
	nextID int
}

func NewTaskStore() *TaskStore {
	return &TaskStore{nextID: 1}
}

func (s *TaskStore) Create(subject, description, activeForm string) *TaskItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &TaskItem{
		ID:          s.nextID,
		Subject:     subject,
		Description: description,
		ActiveForm:  activeForm,
		Status:      TaskPending,
	}
	s.nextID++
	s.tasks = append(s.tasks, t)
	return t
}

func (s *TaskStore) Get(id int) *TaskItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (s *TaskStore) Update(id int, updates map[string]any) (*TaskItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var task *TaskItem
	for _, t := range s.tasks {
		if t.ID == id {
			task = t
			break
		}
	}
	if task == nil {
		return nil, fmt.Errorf("task %d not found", id)
	}

	if status, ok := updates["status"].(string); ok {
		switch TaskStatus(status) {
		case TaskPending, TaskInProgress, TaskCompleted:
			task.Status = TaskStatus(status)
		case "deleted":
			// Remove from list
			for i, t := range s.tasks {
				if t.ID == id {
					s.tasks = append(s.tasks[:i], s.tasks[i+1:]...)
					return task, nil
				}
			}
		default:
			return nil, fmt.Errorf("invalid status %q", status)
		}
	}

	if subject, ok := updates["subject"].(string); ok && subject != "" {
		task.Subject = subject
	}
	if desc, ok := updates["description"].(string); ok {
		task.Description = desc
	}
	if af, ok := updates["active_form"].(string); ok {
		task.ActiveForm = af
	}

	// Handle blockedBy additions
	if addBlockedBy, ok := updates["add_blocked_by"]; ok {
		if arr, ok := addBlockedBy.([]interface{}); ok {
			for _, v := range arr {
				if fv, ok := v.(float64); ok {
					bid := int(fv)
					// Don't add duplicates
					found := false
					for _, existing := range task.BlockedBy {
						if existing == bid {
							found = true
							break
						}
					}
					if !found {
						task.BlockedBy = append(task.BlockedBy, bid)
					}
				}
			}
		}
	}

	return task, nil
}

func (s *TaskStore) List() []*TaskItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*TaskItem, len(s.tasks))
	copy(result, s.tasks)
	return result
}

func (s *TaskStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tasks)
}

// ActiveTask returns the first in_progress task (for spinner display).
func (s *TaskStore) ActiveTask() *TaskItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tasks {
		if t.Status == TaskInProgress {
			return t
		}
	}
	return nil
}

// Stats returns (pending, inProgress, completed) counts.
func (s *TaskStore) Stats() (int, int, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var p, ip, c int
	for _, t := range s.tasks {
		switch t.Status {
		case TaskPending:
			p++
		case TaskInProgress:
			ip++
		case TaskCompleted:
			c++
		}
	}
	return p, ip, c
}

// IsBlocked returns true if a task has unresolved dependencies.
func (s *TaskStore) IsBlocked(t *TaskItem) bool {
	if len(t.BlockedBy) == 0 {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, bid := range t.BlockedBy {
		for _, other := range s.tasks {
			if other.ID == bid && other.Status != TaskCompleted {
				return true
			}
		}
	}
	return false
}

// ──────────────────────────────────────────────────────────────
//  Tool handlers for task management
// ──────────────────────────────────────────────────────────────

func toolCreateTask(args map[string]interface{}, store *TaskStore) (string, bool) {
	subject := getString(args, "subject")
	if subject == "" {
		return "Error: subject is required", false
	}
	description := getString(args, "description")
	activeForm := getString(args, "active_form")
	if activeForm == "" {
		// Auto-generate from subject (crude but useful)
		activeForm = subject + "..."
	}

	task := store.Create(subject, description, activeForm)

	result, _ := json.Marshal(map[string]any{
		"id":      task.ID,
		"subject": task.Subject,
		"status":  task.Status,
	})
	return string(result), true
}

func toolUpdateTask(args map[string]interface{}, store *TaskStore) (string, bool) {
	idFloat, ok := getFloat(args, "task_id")
	if !ok {
		return "Error: task_id is required", false
	}
	id := int(idFloat)

	task, err := store.Update(id, args)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	result, _ := json.Marshal(map[string]any{
		"id":      task.ID,
		"subject": task.Subject,
		"status":  task.Status,
	})
	return string(result), true
}

func toolListTasks(args map[string]interface{}, store *TaskStore) (string, bool) {
	tasks := store.List()
	if len(tasks) == 0 {
		return "No tasks created yet.", true
	}

	var sb strings.Builder
	pending, inProgress, completed := store.Stats()
	sb.WriteString(fmt.Sprintf("Tasks: %d pending, %d in progress, %d completed\n\n", pending, inProgress, completed))
	for _, t := range tasks {
		icon := "○"
		switch t.Status {
		case TaskInProgress:
			icon = "◐"
		case TaskCompleted:
			icon = "✓"
		}
		blocked := ""
		if store.IsBlocked(t) {
			icon = "⊘"
			blocked = fmt.Sprintf(" [blocked by: %v]", t.BlockedBy)
		}
		sb.WriteString(fmt.Sprintf("%s #%d: %s (%s)%s\n", icon, t.ID, t.Subject, t.Status, blocked))
	}
	return sb.String(), true
}

func toolGetTask(args map[string]interface{}, store *TaskStore) (string, bool) {
	idFloat, ok := getFloat(args, "task_id")
	if !ok {
		return "Error: task_id is required", false
	}
	id := int(idFloat)

	task := store.Get(id)
	if task == nil {
		return fmt.Sprintf("Error: task %d not found", id), false
	}

	result, _ := json.MarshalIndent(task, "", "  ")
	return string(result), true
}

// ──────────────────────────────────────────────────────────────
//  tool.TaskManager interface implementation
//
//  These adapter methods let TaskStore be injected into tools via
//  the Env struct without the internal/tool package knowing about
//  TaskStore's internals.
// ──────────────────────────────────────────────────────────────

func (s *TaskStore) CreateTask(args map[string]any) (string, bool) { return toolCreateTask(args, s) }
func (s *TaskStore) UpdateTask(args map[string]any) (string, bool) { return toolUpdateTask(args, s) }
func (s *TaskStore) ListTasks(args map[string]any) (string, bool)  { return toolListTasks(args, s) }
func (s *TaskStore) GetTask(args map[string]any) (string, bool)    { return toolGetTask(args, s) }
