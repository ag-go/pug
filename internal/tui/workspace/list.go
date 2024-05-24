package workspace

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/leg100/pug/internal/module"
	"github.com/leg100/pug/internal/resource"
	"github.com/leg100/pug/internal/run"
	"github.com/leg100/pug/internal/state"
	"github.com/leg100/pug/internal/tui"
	"github.com/leg100/pug/internal/tui/keys"
	"github.com/leg100/pug/internal/tui/navigator"
	tuirun "github.com/leg100/pug/internal/tui/run"
	"github.com/leg100/pug/internal/tui/table"
	tuitask "github.com/leg100/pug/internal/tui/task"
	"github.com/leg100/pug/internal/workspace"
)

var currentColumn = table.Column{
	Key:        "current",
	Title:      "CURRENT",
	Width:      len("CURRENT"),
	FlexFactor: 1,
}

type ListMaker struct {
	ModuleService    tui.ModuleService
	WorkspaceService tui.WorkspaceService
	RunService       tui.RunService
	Helpers          *tui.Helpers
}

func (m *ListMaker) Make(parent resource.Resource, width, height int) (tea.Model, error) {
	var columns []table.Column
	if parent.GetKind() == resource.Global {
		// Show module column in global workspaces table
		columns = append(columns, table.ModuleColumn)
	}
	columns = append(columns,
		table.WorkspaceColumn,
		currentColumn,
		table.ResourceCountColumn,
		table.RunStatusColumn,
		table.RunChangesColumn,
	)

	renderer := func(ws *workspace.Workspace) table.RenderedRow {
		return table.RenderedRow{
			table.ModuleColumn.Key:        ws.ModulePath(),
			table.WorkspaceColumn.Key:     ws.Name,
			table.RunStatusColumn.Key:     m.Helpers.WorkspaceCurrentRunStatus(ws),
			table.RunChangesColumn.Key:    m.Helpers.WorkspaceCurrentRunChanges(ws),
			table.ResourceCountColumn.Key: m.Helpers.WorkspaceResourceCount(ws),
			currentColumn.Key:             m.Helpers.WorkspaceCurrentCheckmark(ws),
		}
	}

	table := table.New(columns, renderer, width, height).
		WithSortFunc(workspace.Sort(m.ModuleService)).
		WithParent(parent)

	return list{
		table:   table,
		svc:     m.WorkspaceService,
		modules: m.ModuleService,
		runs:    m.RunService,
		parent:  parent,
		helpers: m.Helpers,
	}, nil
}

type list struct {
	table   table.Model[resource.ID, *workspace.Workspace]
	svc     tui.WorkspaceService
	modules tui.ModuleService
	runs    tui.RunService
	parent  resource.Resource
	helpers *tui.Helpers
}

func (m list) Init() tea.Cmd {
	return func() tea.Msg {
		workspaces := m.svc.List(workspace.ListOptions{
			ModuleID: m.parent.GetID(),
		})
		return table.BulkInsertMsg[*workspace.Workspace](workspaces)
	}
}

func (m list) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd              tea.Cmd
		cmds             []tea.Cmd
		createRunOptions run.CreateOptions
	)

	switch msg := msg.(type) {
	case resource.Event[*module.Module]:
		// Update changes to current workspace for a module
		m.table.UpdateViewport()
	case resource.Event[*run.Run]:
		// Update current run status and changes
		m.table.UpdateViewport()
	case resource.Event[*state.State]:
		// Update resource counts
		m.table.UpdateViewport()
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.Common.Run):
			fallthrough
		case key.Matches(msg, keys.Global.Enter):
			if row, ok := m.table.CurrentRow(); ok {
				return m, navigator.Go(tui.RunListKind, navigator.WithResource(row.Value))
			}
		case key.Matches(msg, keys.Common.State):
			if row, ok := m.table.CurrentRow(); ok {
				return m, navigator.Go(tui.StateKind, navigator.WithResource(row.Value))
			}
		case key.Matches(msg, keys.Common.Tasks):
			if row, ok := m.table.CurrentRow(); ok {
				return m, navigator.Go(tui.TaskListKind, navigator.WithResource(row.Value))
			}
		case key.Matches(msg, keys.Common.Delete):
			workspaceIDs := m.table.SelectedOrCurrentKeys()
			if len(workspaceIDs) == 0 {
				return m, nil
			}
			return m, tui.YesNoPrompt(
				fmt.Sprintf("Delete %d workspace(s)?", len(workspaceIDs)),
				tuitask.CreateTasks("delete-workspace", m.parent, m.svc.Delete, workspaceIDs...),
			)
		case key.Matches(msg, keys.Common.Init):
			cmd := tuitask.CreateTasks("init", m.parent, m.modules.Init, m.selectedOrCurrentModuleIDs()...)
			return m, cmd
		case key.Matches(msg, keys.Common.Format):
			cmd := tuitask.CreateTasks("format", m.parent, m.modules.Format, m.selectedOrCurrentModuleIDs()...)
			return m, cmd
		case key.Matches(msg, keys.Common.Validate):
			cmd := tuitask.CreateTasks("validate", m.parent, m.modules.Validate, m.selectedOrCurrentModuleIDs()...)
			return m, cmd
		case key.Matches(msg, localKeys.SetCurrent):
			if row, ok := m.table.CurrentRow(); ok {
				return m, func() tea.Msg {
					if err := m.svc.SelectWorkspace(row.Value.ModuleID(), row.Value.ID); err != nil {
						return tui.NewErrorMsg(err, "setting current workspace")
					}
					return nil
				}
			}
		case key.Matches(msg, keys.Common.Destroy):
			createRunOptions.Destroy = true
			fallthrough
		case key.Matches(msg, keys.Common.Plan):
			workspaceIDs := m.table.SelectedOrCurrentKeys()
			return m, tuirun.CreateRuns(m.runs, m.parent, createRunOptions, workspaceIDs...)
		case key.Matches(msg, keys.Common.Apply):
			runIDs, err := m.table.Prune(func(ws *workspace.Workspace) (resource.ID, error) {
				if runID := ws.CurrentRunID; runID != nil {
					return *runID, nil
				}
				return resource.ID{}, errors.New("workspace does not have a current run")
			})
			if err != nil {
				return m, tui.ReportError(err, "")
			}
			return m, tuirun.ApplyCommand(m.runs, m.parent, runIDs...)
		}
	}

	// Handle keyboard and mouse events in the table widget
	m.table, cmd = m.table.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m list) Title() string {
	return tui.Breadcrumbs("Workspaces", m.parent, m.table.TotalString())
}

func (m list) View() string {
	return m.table.View()
}

func (m list) TabStatus() string {
	return fmt.Sprintf("(%s)", m.table.TotalString())
}

func (m list) HelpBindings() []key.Binding {
	return []key.Binding{
		keys.Common.Init,
		keys.Common.Format,
		keys.Common.Validate,
		keys.Common.Plan,
		keys.Common.Apply,
		keys.Common.Destroy,
		keys.Common.State,
		keys.Common.Tasks,
		localKeys.SetCurrent,
	}
}

// selectedOrCurrentModuleIDs returns the IDs of the modules of the
// current or selected workspaces.
func (m list) selectedOrCurrentModuleIDs() []resource.ID {
	selected := m.table.SelectedOrCurrent()
	moduleIDs := make([]resource.ID, len(selected))
	for i, row := range selected {
		moduleIDs[i] = row.Value.ModuleID()
	}
	return moduleIDs
}
