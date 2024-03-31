package task

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/leg100/pug/internal/pubsub"
	"github.com/leg100/pug/internal/resource"
)

type Service struct {
	Broker *pubsub.Broker[*Task]

	table   *resource.Table[*Task]
	counter *int

	*factory
}

type ServiceOptions struct {
	Program string
}

func NewService(opts ServiceOptions) *Service {
	var counter int

	broker := pubsub.NewBroker[*Task]()
	factory := &factory{
		publisher: broker,
		counter:   &counter,
		program:   opts.Program,
	}

	svc := &Service{
		table:   resource.NewTable(broker),
		Broker:  broker,
		factory: factory,
		counter: &counter,
	}
	return svc
}

// Create a task. The task is placed into a pending state and requires enqueuing
// before it'll be processed.
func (s *Service) Create(opts CreateOptions) (*Task, error) {
	task, err := s.newTask(opts)
	if err != nil {
		return nil, err
	}

	// Add to db
	s.table.Add(task.ID, task)
	// Increment counter of number of live tasks
	*s.counter++

	if opts.AfterCreate != nil {
		opts.AfterCreate(task)
	}

	go func() {
		if err := task.Wait(); err != nil {
			// TODO: log error
			return
		}
	}()

	slog.Debug("created task", "task", task)
	return task, nil
}

// Enqueue moves the task onto the global queue for processing.
func (s *Service) Enqueue(taskID resource.ID) (*Task, error) {
	task, err := s.table.Get(taskID)
	if err != nil {
		slog.Error("enqueuing task", "error", err)
		return nil, fmt.Errorf("enqueuing task: %w", err)
	}

	task.updateState(Queued)
	slog.Debug("enqueued task", "task", task)
	return task, nil
}

type ListOptions struct {
	// Filter tasks by those with a matching module path. Optional.
	Path *string
	// Filter tasks by status: match task if it has one of these statuses.
	// Optional.
	Status []Status
	// Order tasks by oldest first (true), or newest first (false)
	Oldest bool
	// Filter tasks by only those that are blocking. If false, both blocking and
	// non-blocking tasks are returned.
	Blocking bool
	// Filter tasks by those with one of the following commands
	Command [][]string
	// Filter tasks by only those that have an ancestor with the given ID.
	// Defaults the zero value, which is the ID of the abstract global entity to
	// which all resources belong.
	Ancestor resource.ID
}

type taskLister interface {
	List(opts ListOptions) []*Task
}

func (s *Service) List(opts ListOptions) []*Task {
	tasks := s.table.List()

	// Filter list according to options
	var i int
	for _, t := range tasks {
		if opts.Path != nil && *opts.Path != t.Path {
			continue
		}
		if opts.Status != nil {
			if !slices.Contains(opts.Status, t.State) {
				continue
			}
		}
		if opts.Blocking {
			if !t.Blocking {
				continue
			}
		}
		if opts.Command != nil {
			for _, cmd := range opts.Command {
				if slices.Equal(cmd, t.Command) {
					break
				}
			}
		}
		if !t.HasAncestor(opts.Ancestor) {
			continue
		}
		tasks[i] = t
		i++
	}
	tasks = tasks[:i]

	// Sort list according to options
	slices.SortFunc(tasks, func(a, b *Task) int {
		cmp := a.Updated.Compare(b.Updated)
		if opts.Oldest {
			return cmp
		}
		return -cmp
	})

	return tasks
}

func (s *Service) Get(taskID resource.ID) (*Task, error) {
	return s.table.Get(taskID)
}

func (s *Service) Subscribe(ctx context.Context) <-chan resource.Event[*Task] {
	return s.Broker.Subscribe(ctx)
}

func (s *Service) Cancel(taskID resource.ID) (*Task, error) {
	task, err := s.table.Get(taskID)
	if err != nil {
		slog.Error("canceling task", "id", taskID)
		return nil, err
	}

	task.cancel()

	slog.Info("canceled task", "task", task)
	return task, nil
}

func (s *Service) Delete(taskID resource.ID) error {
	// TODO: only allow deleting task if in finished state (error message should
	// instruct user to cancel task first).
	s.table.Delete(taskID)
	return nil
}

func (s *Service) Counter() int {
	return *s.counter
}
