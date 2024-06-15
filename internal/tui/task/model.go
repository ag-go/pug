package task

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/leg100/pug/internal/logging"
	"github.com/leg100/pug/internal/resource"
	"github.com/leg100/pug/internal/run"
	"github.com/leg100/pug/internal/task"
	"github.com/leg100/pug/internal/tui"
	"github.com/leg100/pug/internal/tui/keys"
)

// MakerID uniquely identifies a task model maker
type MakerID int

const (
	TaskMakerID MakerID = iota
	TaskListMakerID
	TaskGroupMakerID
)

type Maker struct {
	RunService  tui.RunService
	TaskService tui.TaskService
	Spinner     *spinner.Model
	Helpers     *tui.Helpers
	Logger      *logging.Logger

	disableAutoscroll bool
	showInfo          bool
}

func (mm *Maker) Make(res resource.Resource, width, height int) (tea.Model, error) {
	return mm.makeWithID(res, width, height, TaskMakerID, true)
}

func (mm *Maker) makeWithID(res resource.Resource, width, height int, makerID MakerID, border bool) (tea.Model, error) {
	task, ok := res.(*task.Task)
	if !ok {
		return model{}, errors.New("fatal: cannot make task model with a non-task resource")
	}

	m := model{
		svc:     mm.TaskService,
		runs:    mm.RunService,
		task:    task,
		output:  task.NewReader(),
		spinner: mm.Spinner,
		makerID: makerID,
		// read upto 1kb at a time
		buf:      make([]byte, 1024),
		helpers:  mm.Helpers,
		showInfo: mm.showInfo,
		border:   border,
		width:    width,
	}
	m.setHeight(height)

	if rr := m.task.Run(); rr != nil {
		m.run = rr.(*run.Run)
	}

	m.viewport = tui.NewViewport(tui.ViewportOptions{
		JSON:       m.task.JSON,
		Autoscroll: !mm.disableAutoscroll,
		Width:      m.viewportWidth(),
		Height:     m.height,
		Spinner:    m.spinner,
	})

	return m, nil
}

func (mm *Maker) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, localKeys.Autoscroll):
			mm.disableAutoscroll = !mm.disableAutoscroll

			// Inform user, and send out message to all cached task models to
			// toggle autoscroll.
			return tea.Batch(
				tui.CmdHandler(toggleAutoscrollMsg{}),
				tui.ReportInfo("Toggled autoscroll %s", boolToOnOff(!mm.disableAutoscroll)),
			)
		case key.Matches(msg, localKeys.ToggleInfo):
			mm.showInfo = !mm.showInfo

			// Send out message to all cached task models to toggle task info
			return tui.CmdHandler(toggleTaskInfoMsg{})
		}
	}
	return nil
}

type model struct {
	svc  tui.TaskService
	task *task.Task
	run  *run.Run
	runs tui.RunService

	output  io.Reader
	buf     []byte
	makerID MakerID

	showInfo bool
	border   bool

	viewport tui.Viewport
	spinner  *spinner.Model

	height int
	width  int

	helpers *tui.Helpers
}

func (m model) Init() tea.Cmd {
	return m.getOutput
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.Common.Cancel):
			return m, m.helpers.CreateTasks("cancel", m.svc.Cancel, m.task.ID)
		case key.Matches(msg, keys.Common.Apply):
			if m.run != nil {
				// Only trigger an apply if run is in the planned state
				if m.run.Status != run.Planned {
					return m, nil
				}
				return m, tui.YesNoPrompt(
					"Apply plan?",
					m.helpers.CreateTasks("apply", m.runs.ApplyPlan, m.run.ID),
				)
			}
		case key.Matches(msg, keys.Common.State):
			if ws := m.helpers.TaskWorkspace(m.task); ws != nil {
				return m, tui.NavigateTo(tui.ResourceListKind, tui.WithParent(ws))
			}
		}
	case toggleAutoscrollMsg:
		m.viewport.Autoscroll = !m.viewport.Autoscroll
	case toggleTaskInfoMsg:
		m.showInfo = !m.showInfo
		// adjust width of viewport to accomodate info
		m.viewport.SetDimensions(m.viewportWidth(), m.height)
	case outputMsg:
		// Ensure output is for this task
		if msg.taskID != m.task.ID {
			return m, nil
		}
		// Ensure output is for a task model made by the expected maker (avoids
		// duplicate output where there are multiple models for the same task).
		if msg.makerID != m.makerID {
			return m, nil
		}
		if err := m.viewport.AppendContent(msg.output, msg.eof); err != nil {
			return m, tui.ReportError(err)
		}
		if !msg.eof {
			cmds = append(cmds, m.getOutput)
		}
	case resource.Event[*task.Task]:
		if msg.Payload.ID != m.task.ID {
			// Ignore event for different task.
			return m, nil
		}
		m.task = msg.Payload
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.setHeight(msg.Height)
		m.viewport.SetDimensions(m.viewportWidth(), m.height)
		return m, nil
	}

	// Handle keyboard and mouse events in the viewport
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m model) viewportWidth() int {
	if m.border {
		m.width -= 2
	}
	if m.showInfo {
		m.width -= infoWidth
	}
	return max(0, m.width)
}

func (m *model) setHeight(height int) {
	if m.border {
		height -= 2
	}
	m.height = height
}

const (
	// infoWidth is the width of the optional task info sidebar to the left of the
	// viewport.
	infoWidth = 35
)

// View renders the viewport
func (m model) View() string {
	var components []string

	if m.showInfo {
		var (
			args = "-"
			envs = "-"
		)
		if len(m.task.Args) > 0 {
			args = strings.Join(m.task.Args, "\n")
		}
		if len(m.task.AdditionalEnv) > 0 {
			envs = strings.Join(m.task.AdditionalEnv, "\n")
		}

		// Show info to the left of the viewport.
		content := lipgloss.JoinVertical(lipgloss.Top,
			tui.Bold.Render("Task ID"),
			m.task.ID.String(),
			"",
			tui.Bold.Render("Command"),
			m.task.CommandString(),
			"",
			tui.Bold.Render("Arguments"),
			args,
			"",
			tui.Bold.Render("Environment variables"),
			envs,
			"",
			fmt.Sprintf("Autoscroll: %s", boolToOnOff(m.viewport.Autoscroll)),
		)
		container := tui.Regular.Copy().
			Margin(0, 1).
			// Border to the right, dividing the info from the viewport
			Border(lipgloss.NormalBorder(), false, true, false, false).
			BorderForeground(tui.LighterGrey).
			// Subtract 2 to account for margins, and 1 for border
			Height(m.height).
			// Subtract 2 to account for margins, and 1 for border
			Width(max(infoWidth - 2 - 1)).
			Render(content)
		components = append(components, container)
	}
	components = append(components, m.viewport.View())
	content := lipgloss.JoinHorizontal(lipgloss.Left, components...)
	if m.border {
		return tui.Border.Render(content)
	}
	return content
}

func boolToOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (m model) Title() string {
	return tui.Breadcrumbs("Task", m.task)
}

func (m model) Status() string {
	if m.run != nil {
		return lipgloss.JoinHorizontal(lipgloss.Top,
			m.helpers.TaskStatus(m.task, true),
			" | ",
			m.helpers.LatestRunReport(m.run),
			" ",
			m.helpers.RunStatus(m.run, true),
		)
	}
	return m.helpers.TaskStatus(m.task, true)
}

func (m model) HelpBindings() []key.Binding {
	bindings := []key.Binding{
		keys.Common.Cancel,
	}
	if mod := m.task.Module(); mod != nil {
		bindings = append(bindings, keys.Common.Module)
	}
	if ws := m.task.Workspace(); ws != nil {
		bindings = append(bindings, keys.Common.Workspace)
	}
	if m.run != nil {
		bindings = append(bindings, keys.Common.Run)
		if m.run.Status == run.Planned {
			bindings = append(bindings, keys.Common.Apply)
		}
	}
	return bindings
}

func (m model) getOutput() tea.Msg {
	msg := outputMsg{taskID: m.task.ID, makerID: m.makerID}

	n, err := m.output.Read(m.buf)
	if err == io.EOF {
		msg.eof = true
	} else if err != nil {
		return tui.ReportError(errors.New("reading task output"))()
	}
	msg.output = string(m.buf[:n])
	return msg
}

type outputMsg struct {
	makerID MakerID
	taskID  resource.ID
	output  string
	eof     bool
}
