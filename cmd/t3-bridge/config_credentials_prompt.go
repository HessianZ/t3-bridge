package main

import (
	"errors"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type appCredentialPrompt struct {
	input    textinput.Model
	accepted bool
}

func promptAppCredential(file *os.File) (string, error) {
	input := textinput.New()
	input.Prompt = "App Secret: "
	input.Width = 48
	input.EchoMode = textinput.EchoPassword
	input.CharLimit = maxAppCredentialBytes + 1
	input.KeyMap.Paste.SetEnabled(false)
	model, err := tea.NewProgram(&appCredentialPrompt{input: input}, tea.WithInput(file), tea.WithOutput(os.Stderr)).Run()
	if err != nil || !model.(*appCredentialPrompt).accepted {
		return "", errors.New("App Secret entry canceled or unavailable")
	}
	return model.(*appCredentialPrompt).input.Value(), nil
}

func (prompt *appCredentialPrompt) Init() tea.Cmd { return prompt.input.Focus() }

func (prompt *appCredentialPrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyEnter:
			prompt.accepted = true
			return prompt, tea.Quit
		case tea.KeyEsc, tea.KeyCtrlC:
			prompt.input.Reset()
			return prompt, tea.Quit
		}
		if strings.ContainsAny(string(key.Runes), "\x00\r\n\t") {
			prompt.input.Reset()
			return prompt, nil
		}
	}
	var cmd tea.Cmd
	prompt.input, cmd = prompt.input.Update(msg)
	return prompt, cmd
}

func (prompt *appCredentialPrompt) View() string { return prompt.input.View() }
