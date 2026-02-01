package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"gopkg.in/yaml.v3"
)

const (
	envVarName     = "HYDRA_OVERRIDES"
	overridesDir   = "~/.config/tbp/overrides"
	projectEnvFile = ".envrc"
)

// Override represents a single Hydra override configuration
type Override struct {
	Name       string
	Type       string // "merge" or "replace"
	Block      string // e.g., "test.config.logging"
	File       string // e.g., "override.yaml"
	Content    string // content of override.yaml
	ApplyInfo  string // content of apply.md
	FolderPath string // full path to override folder
}

// App holds the application state
type App struct {
	app             *tview.Application
	overrides       []*Override
	applied         map[string]bool
	availableList   *tview.List
	appliedList     *tview.List
	infoView        *tview.TextView
	contentView     *tview.TextView
	statusBar       *tview.TextView
	panels          []tview.Primitive
	currentPanelIdx int
	projectRoot     string
	helpOpen        bool
}

func main() {
	app := &App{
		applied:     make(map[string]bool),
		projectRoot: getProjectRoot(),
	}

	// Load overrides from disk
	if err := app.loadOverrides(); err != nil {
		fmt.Fprintf(os.Stderr, "Error loading overrides: %v\n", err)
		os.Exit(1)
	}

	// Load persisted state from .envrc
	if err := app.loadPersistedState(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load persisted state: %v\n", err)
	}

	// Check for --help flag
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		fmt.Println(`LazyHydra - Lazygit-style TUI for managing Hydra CLI overrides

Usage:
  lazyhydra           Launch the TUI
  lazyhydra -l        List all overrides and their status
  lazyhydra -p        Print the current override string (for use in scripts)
  lazyhydra -h        Show this help

Environment:
  PROJECT_ROOT        Directory for .envrc file (default: current directory)

Overrides are loaded from: ~/.config/tbp/overrides/
Each override folder should contain:
  - override.yaml     The override configuration
  - apply.md          Metadata (type, block, file) in YAML frontmatter

Keybindings in TUI:
  1, 2, 3             Jump to panel
  Tab / Shift+Tab     Cycle panels
  h / l               Previous / Next panel
  j / k               Move cursor up / down
  Space / Enter       Apply or remove override
  ?                   Show help
  q / Esc             Quit`)
		return
	}

	// Check for --list flag to print overrides without TUI
	if len(os.Args) > 1 && (os.Args[1] == "--list" || os.Args[1] == "-l") {
		fmt.Println("Available overrides:")
		for _, o := range app.overrides {
			status := "[ ]"
			if app.applied[o.Name] {
				status = "[x]"
			}
			fmt.Printf("  %s %s (type: %s, block: %s)\n", status, o.Name, o.Type, o.Block)
		}
		if len(app.getAppliedOverrides()) > 0 {
			fmt.Printf("\nOverride string:\n  %s\n", app.buildOverrideString())
		}
		return
	}

	// Check for --print flag to only print override string
	if len(os.Args) > 1 && (os.Args[1] == "--print" || os.Args[1] == "-p") {
		fmt.Print(app.buildOverrideString())
		return
	}

	app.setupUI()
	app.refreshAll()

	if err := app.app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func getProjectRoot() string {
	if root := os.Getenv("PROJECT_ROOT"); root != "" {
		return root
	}
	dir, _ := os.Getwd()
	return dir
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func (app *App) loadOverrides() error {
	dir := expandPath(overridesDir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading overrides directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		overridePath := filepath.Join(dir, entry.Name())
		applyPath := filepath.Join(overridePath, "apply.md")
		overrideYAMLPath := filepath.Join(overridePath, "override.yaml")

		applyContent, err := os.ReadFile(applyPath)
		if err != nil {
			continue
		}

		override := &Override{
			Name:       entry.Name(),
			FolderPath: overridePath,
			ApplyInfo:  string(applyContent),
		}

		content := string(applyContent)
		if strings.HasPrefix(content, "---") {
			parts := strings.SplitN(content[3:], "---", 2)
			if len(parts) >= 1 {
				var meta struct {
					Type  string `yaml:"type"`
					Block string `yaml:"block"`
					File  string `yaml:"file"`
				}
				if err := yaml.Unmarshal([]byte(parts[0]), &meta); err == nil {
					override.Type = meta.Type
					override.Block = meta.Block
					override.File = meta.File
				}
			}
		}

		if overrideContent, err := os.ReadFile(overrideYAMLPath); err == nil {
			override.Content = string(overrideContent)
		}

		app.overrides = append(app.overrides, override)
	}

	sort.Slice(app.overrides, func(i, j int) bool {
		return app.overrides[i].Name < app.overrides[j].Name
	})

	return nil
}

func (app *App) loadPersistedState() error {
	envrcPath := filepath.Join(app.projectRoot, projectEnvFile)

	file, err := os.Open(envrcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "export "+envVarName+"=") {
			value := strings.TrimPrefix(line, "export "+envVarName+"=")
			value = strings.Trim(value, "\"'")

			if value == "" {
				return nil
			}

			decoded, err := base64.StdEncoding.DecodeString(value)
			if err != nil {
				return fmt.Errorf("decoding persisted state: %w", err)
			}

			names := strings.Split(string(decoded), ",")
			for _, name := range names {
				name = strings.TrimSpace(name)
				if name != "" {
					app.applied[name] = true
				}
			}
			break
		}
	}

	return scanner.Err()
}

func (app *App) savePersistedState() error {
	envrcPath := filepath.Join(app.projectRoot, projectEnvFile)

	var lines []string
	existingFile, err := os.Open(envrcPath)
	if err == nil {
		scanner := bufio.NewScanner(existingFile)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "export "+envVarName+"=") &&
				!strings.HasPrefix(line, "export HYDRA_OVERRIDE_STR=") {
				lines = append(lines, line)
			}
		}
		existingFile.Close()
	}

	var appliedNames []string
	for _, o := range app.overrides {
		if app.applied[o.Name] {
			appliedNames = append(appliedNames, o.Name)
		}
	}

	if len(appliedNames) > 0 {
		encoded := base64.StdEncoding.EncodeToString([]byte(strings.Join(appliedNames, ",")))
		lines = append(lines, fmt.Sprintf("export %s=\"%s\"", envVarName, encoded))

		overrideStr := app.buildOverrideString()
		lines = append(lines, fmt.Sprintf("export HYDRA_OVERRIDE_STR=\"%s\"", overrideStr))
	}

	return os.WriteFile(envrcPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func (app *App) buildOverrideString() string {
	var parts []string

	for _, o := range app.overrides {
		if !app.applied[o.Name] {
			continue
		}

		prefix := "+"
		if o.Type == "replace" {
			prefix = ""
		}

		blockParts := strings.Split(o.Block, ".")
		lastBlockPart := blockParts[len(blockParts)-1]

		overrideStr := fmt.Sprintf("%sexperiment/config/%s@%s=%s",
			prefix, lastBlockPart, o.Block, o.Name)
		parts = append(parts, overrideStr)
	}

	return strings.Join(parts, " ")
}

func (app *App) setupUI() {
	app.app = tview.NewApplication()

	// Create Available Overrides list
	app.availableList = tview.NewList().
		ShowSecondaryText(false).
		SetHighlightFullLine(true).
		SetSelectedBackgroundColor(tcell.ColorDarkBlue).
		SetSelectedTextColor(tcell.ColorWhite)
	app.availableList.SetBorder(true).
		SetTitle(" [1] Available Overrides ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(tcell.ColorDefault)

	// Create Applied Overrides list
	app.appliedList = tview.NewList().
		ShowSecondaryText(false).
		SetHighlightFullLine(true).
		SetSelectedBackgroundColor(tcell.ColorDarkBlue).
		SetSelectedTextColor(tcell.ColorWhite)
	app.appliedList.SetBorder(true).
		SetTitle(" [2] Applied Overrides ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(tcell.ColorDefault)

	// Create Info view
	app.infoView = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true)
	app.infoView.SetBorder(true).
		SetTitle(" [3] Info ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(tcell.ColorDefault)

	// Create Content view
	app.contentView = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true).
		SetScrollable(true)
	app.contentView.SetBorder(true).
		SetTitle(" Override Content ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(tcell.ColorDefault)

	// Create Status bar
	app.statusBar = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)

	// Store panels for navigation
	app.panels = []tview.Primitive{app.availableList, app.appliedList, app.infoView}

	// Left side panels (vertically stacked)
	leftFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(app.availableList, 0, 1, true).
		AddItem(app.appliedList, 0, 1, false).
		AddItem(app.infoView, 0, 1, false)

	// Main layout (horizontal: left panels | content)
	mainFlex := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(leftFlex, 0, 2, true).
		AddItem(app.contentView, 0, 3, false)

	// Root layout with status bar
	rootFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(mainFlex, 0, 1, true).
		AddItem(app.statusBar, 1, 0, false)

	// Set up keybindings
	app.setupKeybindings()

	// Set up list selection handlers
	app.availableList.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		app.updateContentAndInfo()
	})

	app.appliedList.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		app.updateContentAndInfo()
	})

	// Focus handler to update border colors
	app.app.SetFocus(app.availableList)
	app.updateBorderColors()

	app.app.SetRoot(rootFlex, true)
}

func (app *App) setupKeybindings() {
	app.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// If help is open, close it on Escape or q
		if app.helpOpen {
			if event.Key() == tcell.KeyEsc || event.Rune() == 'q' {
				app.closeHelp()
				return nil
			}
			return event
		}

		switch event.Key() {
		case tcell.KeyRune:
			switch event.Rune() {
			case 'q':
				app.app.Stop()
				return nil
			case '1':
				app.focusPanel(0)
				return nil
			case '2':
				app.focusPanel(1)
				return nil
			case '3':
				app.focusPanel(2)
				return nil
			case 'h':
				app.prevPanel()
				return nil
			case 'l':
				app.nextPanel()
				return nil
			case 'j':
				app.cursorDown()
				return nil
			case 'k':
				app.cursorUp()
				return nil
			case ' ':
				app.toggleOverride()
				return nil
			case '?':
				app.showHelp()
				return nil
			}
		case tcell.KeyTab:
			app.nextPanel()
			return nil
		case tcell.KeyBacktab:
			app.prevPanel()
			return nil
		case tcell.KeyEnter:
			app.toggleOverride()
			return nil
		case tcell.KeyLeft:
			app.prevPanel()
			return nil
		case tcell.KeyRight:
			app.nextPanel()
			return nil
		case tcell.KeyEsc:
			app.app.Stop()
			return nil
		}
		return event
	})
}

func (app *App) cursorDown() {
	switch app.currentPanelIdx {
	case 0:
		count := app.availableList.GetItemCount()
		current := app.availableList.GetCurrentItem()
		if current < count-1 {
			app.availableList.SetCurrentItem(current + 1)
		}
	case 1:
		count := app.appliedList.GetItemCount()
		current := app.appliedList.GetCurrentItem()
		if current < count-1 {
			app.appliedList.SetCurrentItem(current + 1)
		}
	}
	app.updateContentAndInfo()
}

func (app *App) cursorUp() {
	switch app.currentPanelIdx {
	case 0:
		current := app.availableList.GetCurrentItem()
		if current > 0 {
			app.availableList.SetCurrentItem(current - 1)
		}
	case 1:
		current := app.appliedList.GetCurrentItem()
		if current > 0 {
			app.appliedList.SetCurrentItem(current - 1)
		}
	}
	app.updateContentAndInfo()
}

func (app *App) focusPanel(idx int) {
	if idx >= 0 && idx < len(app.panels) {
		app.currentPanelIdx = idx
		app.app.SetFocus(app.panels[idx])
		app.updateBorderColors()
		app.updateContentAndInfo()
	}
}

func (app *App) nextPanel() {
	app.currentPanelIdx = (app.currentPanelIdx + 1) % len(app.panels)
	app.app.SetFocus(app.panels[app.currentPanelIdx])
	app.updateBorderColors()
	app.updateContentAndInfo()
}

func (app *App) prevPanel() {
	app.currentPanelIdx = (app.currentPanelIdx - 1 + len(app.panels)) % len(app.panels)
	app.app.SetFocus(app.panels[app.currentPanelIdx])
	app.updateBorderColors()
	app.updateContentAndInfo()
}

func (app *App) updateBorderColors() {
	// Reset all borders to default
	app.availableList.SetBorderColor(tcell.ColorDefault)
	app.appliedList.SetBorderColor(tcell.ColorDefault)
	app.infoView.SetBorderColor(tcell.ColorDefault)
	app.contentView.SetBorderColor(tcell.ColorDefault)

	// Highlight focused panel with green (lazygit style)
	switch app.currentPanelIdx {
	case 0:
		app.availableList.SetBorderColor(tcell.ColorGreen)
	case 1:
		app.appliedList.SetBorderColor(tcell.ColorGreen)
	case 2:
		app.infoView.SetBorderColor(tcell.ColorGreen)
	}
}

func (app *App) toggleOverride() {
	switch app.currentPanelIdx {
	case 0: // Available list - apply override
		idx := app.availableList.GetCurrentItem()
		available := app.getAvailableOverrides()
		if idx >= 0 && idx < len(available) {
			override := available[idx]
			app.applied[override.Name] = true
			app.savePersistedState()
			app.refreshAll()
		}
	case 1: // Applied list - remove override
		idx := app.appliedList.GetCurrentItem()
		applied := app.getAppliedOverrides()
		if idx >= 0 && idx < len(applied) {
			override := applied[idx]
			delete(app.applied, override.Name)
			app.savePersistedState()
			app.refreshAll()
		}
	}
}

func (app *App) getAvailableOverrides() []*Override {
	var list []*Override
	for _, o := range app.overrides {
		if !app.applied[o.Name] {
			list = append(list, o)
		}
	}
	return list
}

func (app *App) getAppliedOverrides() []*Override {
	var list []*Override
	for _, o := range app.overrides {
		if app.applied[o.Name] {
			list = append(list, o)
		}
	}
	return list
}

func (app *App) getSelectedOverride() *Override {
	switch app.currentPanelIdx {
	case 0:
		available := app.getAvailableOverrides()
		idx := app.availableList.GetCurrentItem()
		if idx >= 0 && idx < len(available) {
			return available[idx]
		}
	case 1:
		applied := app.getAppliedOverrides()
		idx := app.appliedList.GetCurrentItem()
		if idx >= 0 && idx < len(applied) {
			return applied[idx]
		}
	}
	// Default: return first available or applied
	if len(app.overrides) > 0 {
		return app.overrides[0]
	}
	return nil
}

func (app *App) refreshAll() {
	// Refresh available list
	currentAvailableIdx := app.availableList.GetCurrentItem()
	app.availableList.Clear()
	available := app.getAvailableOverrides()
	for _, o := range available {
		app.availableList.AddItem(o.Name, "", 0, nil)
	}
	if currentAvailableIdx >= len(available) {
		currentAvailableIdx = len(available) - 1
	}
	if currentAvailableIdx >= 0 {
		app.availableList.SetCurrentItem(currentAvailableIdx)
	}

	// Refresh applied list
	currentAppliedIdx := app.appliedList.GetCurrentItem()
	app.appliedList.Clear()
	applied := app.getAppliedOverrides()
	for _, o := range applied {
		marker := "[green]+[-] "
		if o.Type == "replace" {
			marker = "[yellow]=[-] "
		}
		app.appliedList.AddItem(marker+o.Name, "", 0, nil)
	}
	if currentAppliedIdx >= len(applied) {
		currentAppliedIdx = len(applied) - 1
	}
	if currentAppliedIdx >= 0 {
		app.appliedList.SetCurrentItem(currentAppliedIdx)
	}

	app.updateContentAndInfo()
	app.updateStatusBar()
	app.updateBorderColors()
}

func (app *App) updateContentAndInfo() {
	selected := app.getSelectedOverride()

	// Update info view
	app.infoView.Clear()
	if selected == nil {
		app.infoView.SetText("No override selected")
	} else {
		status := "[red]Not applied[-]"
		if app.applied[selected.Name] {
			status = "[green]Applied[-]"
		}
		info := fmt.Sprintf("[yellow]Name:[-] %s\n[yellow]Type:[-] %s\n[yellow]Block:[-] %s\n[yellow]Status:[-] %s",
			selected.Name, selected.Type, selected.Block, status)
		app.infoView.SetText(info)
	}

	// Update content view
	app.contentView.Clear()
	if selected == nil {
		app.contentView.SetText("Select an override to view its content")
	} else {
		content := fmt.Sprintf("[cyan::b]# %s/override.yaml[-:-:-]\n\n%s", selected.Name, selected.Content)
		if selected.ApplyInfo != "" {
			content += fmt.Sprintf("\n\n[yellow::b]# Apply Configuration[-:-:-]\n%s", selected.ApplyInfo)
		}
		app.contentView.SetText(content)
	}
}

func (app *App) updateStatusBar() {
	overrideStr := app.buildOverrideString()
	if overrideStr == "" {
		overrideStr = "(no overrides applied)"
	}

	status := fmt.Sprintf(" [yellow]Overrides:[-] %s [darkgray]| [1-3] panels  [space/enter] toggle  [q] quit  [?] help[-]", overrideStr)
	app.statusBar.SetText(status)
}

func (app *App) showHelp() {
	app.helpOpen = true

	helpText := tview.NewTextView().
		SetDynamicColors(true).
		SetText(`[yellow::b]LazyHydra - Hydra Override Manager[-:-:-]

[green]Navigation:[-]
  1, 2, 3         Jump to panel
  Tab / Shift+Tab Cycle panels
  h / l           Prev / Next panel
  j / k / arrows  Move cursor

[green]Actions:[-]
  Space / Enter   Apply/Remove override
  q               Quit
  ?               Show this help

[green]Persistence:[-]
  Applied overrides are saved to:
  $PROJECT_ROOT/.envrc

[green]Environment Variables:[-]
  HYDRA_OVERRIDES     Encoded applied overrides
  HYDRA_OVERRIDE_STR  Override string for CLI

[darkgray]Press Escape or q to close[-]`)

	helpText.SetBorder(true).
		SetTitle(" Help ").
		SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.ColorGreen)

	// Center the help box
	flex := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(helpText, 22, 0, true).
			AddItem(nil, 0, 1, false), 60, 0, true).
		AddItem(nil, 0, 1, false)

	app.app.SetRoot(flex, true)
	app.app.SetFocus(helpText)
}

func (app *App) closeHelp() {
	app.helpOpen = false
	app.app.SetRoot(app.buildRootLayout(), true)
	app.app.SetFocus(app.panels[app.currentPanelIdx])
	app.updateBorderColors()
}

func (app *App) buildRootLayout() tview.Primitive {
	leftFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(app.availableList, 0, 1, true).
		AddItem(app.appliedList, 0, 1, false).
		AddItem(app.infoView, 0, 1, false)

	mainFlex := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(leftFlex, 0, 2, true).
		AddItem(app.contentView, 0, 3, false)

	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(mainFlex, 0, 1, true).
		AddItem(app.statusBar, 1, 0, false)
}
