package main

import (
	"strings"
	"testing"
)

func TestTaskStoreCreate(t *testing.T) {
	store := NewTaskStore()
	task := store.Create("Fix auth bug", "The login flow is broken", "Fixing auth bug")

	if task.ID != 1 {
		t.Errorf("expected ID=1, got %d", task.ID)
	}
	if task.Subject != "Fix auth bug" {
		t.Errorf("expected subject='Fix auth bug', got %q", task.Subject)
	}
	if task.Status != TaskPending {
		t.Errorf("expected status=pending, got %q", task.Status)
	}
	if task.ActiveForm != "Fixing auth bug" {
		t.Errorf("expected active_form='Fixing auth bug', got %q", task.ActiveForm)
	}

	// Second task gets ID 2
	task2 := store.Create("Add tests", "", "Adding tests")
	if task2.ID != 2 {
		t.Errorf("expected ID=2, got %d", task2.ID)
	}
	if store.Count() != 2 {
		t.Errorf("expected count=2, got %d", store.Count())
	}
}

func TestTaskStoreGet(t *testing.T) {
	store := NewTaskStore()
	store.Create("Task 1", "", "")
	store.Create("Task 2", "", "")

	got := store.Get(2)
	if got == nil {
		t.Fatal("expected task 2, got nil")
	}
	if got.Subject != "Task 2" {
		t.Errorf("expected 'Task 2', got %q", got.Subject)
	}

	if store.Get(99) != nil {
		t.Error("expected nil for non-existent task")
	}
}

func TestTaskStoreUpdate(t *testing.T) {
	store := NewTaskStore()
	store.Create("Fix bug", "", "Fixing bug")

	// Update status to in_progress
	task, err := store.Update(1, map[string]any{"status": "in_progress"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Status != TaskInProgress {
		t.Errorf("expected in_progress, got %q", task.Status)
	}

	// Update to completed
	task, err = store.Update(1, map[string]any{"status": "completed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Status != TaskCompleted {
		t.Errorf("expected completed, got %q", task.Status)
	}

	// Update subject
	task, err = store.Update(1, map[string]any{"subject": "Fixed bug"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Subject != "Fixed bug" {
		t.Errorf("expected 'Fixed bug', got %q", task.Subject)
	}
}

func TestTaskStoreDelete(t *testing.T) {
	store := NewTaskStore()
	store.Create("Task 1", "", "")
	store.Create("Task 2", "", "")

	_, err := store.Update(1, map[string]any{"status": "deleted"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.Count() != 1 {
		t.Errorf("expected count=1 after delete, got %d", store.Count())
	}
	if store.Get(1) != nil {
		t.Error("expected task 1 to be gone after delete")
	}
}

func TestTaskStoreUpdateNotFound(t *testing.T) {
	store := NewTaskStore()
	_, err := store.Update(99, map[string]any{"status": "completed"})
	if err == nil {
		t.Error("expected error for non-existent task")
	}
}

func TestTaskStoreStats(t *testing.T) {
	store := NewTaskStore()
	store.Create("A", "", "")
	store.Create("B", "", "")
	store.Create("C", "", "")

	store.Update(1, map[string]any{"status": "in_progress"})
	store.Update(2, map[string]any{"status": "completed"})

	pending, inProgress, completed := store.Stats()
	if pending != 1 {
		t.Errorf("expected 1 pending, got %d", pending)
	}
	if inProgress != 1 {
		t.Errorf("expected 1 in_progress, got %d", inProgress)
	}
	if completed != 1 {
		t.Errorf("expected 1 completed, got %d", completed)
	}
}

func TestTaskStoreActiveTask(t *testing.T) {
	store := NewTaskStore()
	store.Create("A", "", "Doing A")
	store.Create("B", "", "Doing B")

	if store.ActiveTask() != nil {
		t.Error("expected no active task when all pending")
	}

	store.Update(2, map[string]any{"status": "in_progress"})
	active := store.ActiveTask()
	if active == nil {
		t.Fatal("expected active task")
	}
	if active.ID != 2 {
		t.Errorf("expected active task ID=2, got %d", active.ID)
	}
}

func TestTaskStoreBlockedBy(t *testing.T) {
	store := NewTaskStore()
	store.Create("Setup", "", "")
	store.Create("Build", "", "")

	// Add dependency: task 2 blocked by task 1
	store.Update(2, map[string]any{
		"add_blocked_by": []interface{}{float64(1)},
	})

	task2 := store.Get(2)
	if !store.IsBlocked(task2) {
		t.Error("expected task 2 to be blocked")
	}

	// Complete task 1, task 2 should unblock
	store.Update(1, map[string]any{"status": "completed"})
	if store.IsBlocked(task2) {
		t.Error("expected task 2 to be unblocked after task 1 completed")
	}
}

func TestToolCreateTask(t *testing.T) {
	store := NewTaskStore()
	output, ok := toolCreateTask(map[string]interface{}{
		"subject":     "Write tests",
		"description": "Add unit tests for auth module",
		"active_form": "Writing tests",
	}, store)

	if !ok {
		t.Errorf("expected success, got failure: %s", output)
	}
	if !strings.Contains(output, `"id":1`) && !strings.Contains(output, `"id": 1`) {
		t.Errorf("expected id=1 in output: %s", output)
	}
	if store.Count() != 1 {
		t.Errorf("expected count=1, got %d", store.Count())
	}
}

func TestToolCreateTaskMissingSubject(t *testing.T) {
	store := NewTaskStore()
	_, ok := toolCreateTask(map[string]interface{}{}, store)
	if ok {
		t.Error("expected failure for missing subject")
	}
}

func TestToolUpdateTask(t *testing.T) {
	store := NewTaskStore()
	store.Create("Fix bug", "", "")

	output, ok := toolUpdateTask(map[string]interface{}{
		"task_id": float64(1),
		"status":  "completed",
	}, store)

	if !ok {
		t.Errorf("expected success: %s", output)
	}
	task := store.Get(1)
	if task.Status != TaskCompleted {
		t.Errorf("expected completed, got %q", task.Status)
	}
}

func TestToolListTasks(t *testing.T) {
	store := NewTaskStore()
	store.Create("Task A", "", "")
	store.Create("Task B", "", "")
	store.Update(1, map[string]any{"status": "completed"})

	output, ok := toolListTasks(map[string]interface{}{}, store)
	if !ok {
		t.Errorf("unexpected failure: %s", output)
	}
	if !strings.Contains(output, "1 pending") {
		t.Errorf("expected '1 pending' in output: %s", output)
	}
	if !strings.Contains(output, "1 completed") {
		t.Errorf("expected '1 completed' in output: %s", output)
	}
}

func TestToolGetTask(t *testing.T) {
	store := NewTaskStore()
	store.Create("Detailed task", "Do many things", "Doing things")

	output, ok := toolGetTask(map[string]interface{}{
		"task_id": float64(1),
	}, store)

	if !ok {
		t.Errorf("unexpected failure: %s", output)
	}
	if !strings.Contains(output, "Detailed task") {
		t.Errorf("expected subject in output: %s", output)
	}
	if !strings.Contains(output, "Do many things") {
		t.Errorf("expected description in output: %s", output)
	}
}

func TestToolGetTaskNotFound(t *testing.T) {
	store := NewTaskStore()
	_, ok := toolGetTask(map[string]interface{}{
		"task_id": float64(99),
	}, store)
	if ok {
		t.Error("expected failure for non-existent task")
	}
}
