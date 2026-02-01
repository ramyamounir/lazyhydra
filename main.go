package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"gopkg.in/yaml.v3"
)

// Config holds application configuration loaded from ~/.config/lazyhydra/config.yaml
type Config struct {
	EnvVarName     string `yaml:"env_var_name"`
	OverridesDir   string `yaml:"overrides_dir"`
	ProjectEnvFile string `yaml:"project_env_file"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	return &Config{
		EnvVarName:     "HYDRA_OVERRIDES",
		OverridesDir:   "$PROJECT_ROOT/conf/overrides",
		ProjectEnvFile: ".envrc",
	}
}

func loadConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return DefaultConfig(), nil
	}

	configPath := filepath.Join(home, ".config", "lazyhydra", "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	config := DefaultConfig()
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return config, nil
}

func init() {
	// Set rounded borders globally
	tview.Borders.Horizontal = '─'
	tview.Borders.Vertical = '│'
	tview.Borders.TopLeft = '╭'
	tview.Borders.TopRight = '╮'
	tview.Borders.BottomLeft = '╰'
	tview.Borders.BottomRight = '╯'
}

// highlightCode applies syntax highlighting to code using chroma
func highlightCode(code, language string) string {
	lexer := lexers.Get(language)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	style := styles.Get("gruvbox")
	if style == nil {
		style = styles.Fallback
	}

	var buf strings.Builder
	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return tview.Escape(code)
	}

	for token := iterator(); token != chroma.EOF; token = iterator() {
		entry := style.Get(token.Type)
		text := tview.Escape(token.Value)
		if entry.Colour.IsSet() {
			r, g, b := entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue()
			buf.WriteString(fmt.Sprintf("[#%02x%02x%02x]%s[-]", r, g, b, text))
		} else {
			buf.WriteString(text)
		}
	}
	return buf.String()
}

// Override represents a single Hydra override configuration
type Override struct {
	Name       string
	Type       string // "merge" or "replace"
	Block      string // e.g., "test.config.logging"
	File       string // e.g., "override.yaml"
	ModulePath string // e.g., "overrides/my_override" or "configs/logging" (optional, defaults to "overrides/[name]")
	Module     string // e.g., "override" or "custom_module" (optional, defaults to "override")
	Content    string // content of override.yaml
	ApplyInfo  string // content of apply.md
	FolderPath string // full path to override folder
}

// App holds the application state
type App struct {
	config            *Config
	app               *tview.Application
	pages             *tview.Pages
	overrides         []*Override
	applied           map[string]bool
	availableList     *tview.List
	appliedList       *tview.List
	contentView       *tview.TextView
	overrideStringView *tview.TextView
	statusBar         *tview.TextView
	panels            []tview.Primitive
	currentPanelIdx   int
	projectRoot       string
	helpOpen          bool
	inputOpen         bool
	deleteOpen        bool
	renameOpen        bool
	renameTarget      *Override
}

func main() {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	app := &App{
		config:      config,
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
		fmt.Println(`LazyHydra - Lazy-style TUI for managing Hydra CLI overrides

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
  1, 2                Jump to panel
  Tab / Shift+Tab     Cycle panels
  h / l               Previous / Next panel
  j / k               Move cursor up / down
  Space / Enter       Apply or remove override
  n                   Create new override
  d                   Delete override
  r                   Rename override
  e                   Edit apply.md in $EDITOR
  E                   Edit override.yaml in $EDITOR
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
	if strings.HasPrefix(path, "$PROJECT_ROOT") {
		root := os.Getenv("PROJECT_ROOT")
		if root == "" {
			root, _ = os.Getwd()
		}
		return filepath.Join(root, path[len("$PROJECT_ROOT"):])
	}
	return path
}

func (app *App) loadOverrides() error {
	dir := expandPath(app.config.OverridesDir)

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
					Type       string `yaml:"type"`
					Block      string `yaml:"block"`
					File       string `yaml:"file"`
					ModulePath string `yaml:"module_path"`
					Module     string `yaml:"module"`
				}
				if err := yaml.Unmarshal([]byte(parts[0]), &meta); err == nil {
					override.Type = meta.Type
					override.Block = meta.Block
					override.File = meta.File
					override.ModulePath = meta.ModulePath
					override.Module = meta.Module
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
	envrcPath := filepath.Join(app.projectRoot, app.config.ProjectEnvFile)

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
		if strings.HasPrefix(line, "export "+app.config.EnvVarName+"=") {
			value := strings.TrimPrefix(line, "export "+app.config.EnvVarName+"=")
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
	envrcPath := filepath.Join(app.projectRoot, app.config.ProjectEnvFile)

	var lines []string
	existingFile, err := os.Open(envrcPath)
	if err == nil {
		scanner := bufio.NewScanner(existingFile)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "export "+app.config.EnvVarName+"=") &&
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
		lines = append(lines, fmt.Sprintf("export %s=\"%s\"", app.config.EnvVarName, encoded))
	}

	// Always write HYDRA_OVERRIDE_STR (empty string if no overrides)
	// Join with spaces for .envrc (display uses newlines for readability)
	overrideStr := strings.ReplaceAll(app.buildOverrideString(), "\n", " ")
	lines = append(lines, fmt.Sprintf("export HYDRA_OVERRIDE_STR=\"%s\"", overrideStr))

	if err := os.WriteFile(envrcPath, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		return err
	}

	// Run direnv allow so changes take effect immediately
	cmd := exec.Command("direnv", "allow", app.projectRoot)
	cmd.Dir = app.projectRoot
	return cmd.Run()
}

func (app *App) buildOverrideString() string {
	var parts []string

	for _, o := range app.overrides {
		if !app.applied[o.Name] {
			continue
		}

		// Use custom module_path/module if provided, otherwise use defaults
		modulePath := o.ModulePath
		if modulePath == "" {
			modulePath = fmt.Sprintf("overrides/%s", o.Name)
		}
		module := o.Module
		if module == "" {
			module = "override"
		}

		// Format: [type][module_path]@[block]=[module]
		overrideStr := fmt.Sprintf("%s%s@%s=%s",
			o.Type, modulePath, o.Block, module)
		parts = append(parts, overrideStr)
	}

	return strings.Join(parts, "\n")
}

func (app *App) setupUI() {
	app.app = tview.NewApplication()

	// Lazygit-style blue selection color: #6a9fb5
	selectionColor := tcell.NewRGBColor(106, 159, 181)

	// Create Available Overrides list
	app.availableList = tview.NewList().
		ShowSecondaryText(false).
		SetHighlightFullLine(true).
		SetSelectedBackgroundColor(selectionColor).
		SetSelectedTextColor(tcell.ColorWhite)
	app.availableList.SetBorder(true).
		SetTitle(" [1] Available Overrides ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(tcell.ColorDefault)

	// Create Applied Overrides list
	app.appliedList = tview.NewList().
		ShowSecondaryText(false).
		SetHighlightFullLine(true).
		SetSelectedBackgroundColor(selectionColor).
		SetSelectedTextColor(tcell.ColorWhite)
	app.appliedList.SetBorder(true).
		SetTitle(" [2] Applied Overrides ").
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

	// Create Override String view
	app.overrideStringView = tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true).
		SetScrollable(true)
	app.overrideStringView.SetBorder(true).
		SetTitle(" Override String ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(tcell.ColorDefault)

	// Create Status bar
	app.statusBar = tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)

	// Store panels for navigation (only 1 and 2 are navigable)
	app.panels = []tview.Primitive{app.availableList, app.appliedList}

	// Left side panels (vertically stacked)
	leftFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(app.availableList, 0, 1, true).
		AddItem(app.appliedList, 0, 1, false)

	// Right side panels (vertically stacked)
	rightFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(app.contentView, 0, 3, true).
		AddItem(app.overrideStringView, 0, 1, false)

	// Main layout (horizontal: left panels | right panels)
	mainFlex := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(leftFlex, 0, 2, true).
		AddItem(rightFlex, 0, 3, false)

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

	// Create pages for overlay support
	app.pages = tview.NewPages().
		AddPage("main", rootFlex, true, true)

	app.app.SetRoot(app.pages, true)
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

		// If input is open, close it on Escape
		if app.inputOpen {
			if event.Key() == tcell.KeyEsc {
				app.closeInput()
				return nil
			}
			return event
		}

		// If delete confirmation is open, handle it
		if app.deleteOpen {
			if event.Key() == tcell.KeyEsc || event.Rune() == 'q' {
				app.closeDeleteConfirmation()
				return nil
			}
			if event.Key() == tcell.KeyEnter {
				app.deleteSelectedOverride()
				app.closeDeleteConfirmation()
				return nil
			}
			return event
		}

		// If rename input is open, close it on Escape
		if app.renameOpen {
			if event.Key() == tcell.KeyEsc {
				app.closeRenameInput()
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
			case 'J':
				app.scrollContentDown()
				return nil
			case 'K':
				app.scrollContentUp()
				return nil
			case ' ':
				app.toggleOverride()
				return nil
			case '?':
				app.showHelp()
				return nil
			case 'e':
				app.openInEditor("apply.md")
				return nil
			case 'E':
				app.openInEditor("override.yaml")
				return nil
			case 'n':
				app.showNewOverrideInput()
				return nil
			case 'd':
				app.showDeleteConfirmation()
				return nil
			case 'r':
				app.showRenameInput()
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

func (app *App) scrollContentDown() {
	row, col := app.contentView.GetScrollOffset()
	app.contentView.ScrollTo(row+1, col)
}

func (app *App) scrollContentUp() {
	row, col := app.contentView.GetScrollOffset()
	if row > 0 {
		app.contentView.ScrollTo(row-1, col)
	}
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
	// Lazygit-style blue selection color
	selectionColor := tcell.NewRGBColor(106, 159, 181)

	// Reset all borders to default
	app.availableList.SetBorderColor(tcell.ColorDefault)
	app.appliedList.SetBorderColor(tcell.ColorDefault)
	app.contentView.SetBorderColor(tcell.ColorDefault)
	app.overrideStringView.SetBorderColor(tcell.ColorDefault)

	// Reset selection colors - unfocused lists don't show selection highlight
	app.availableList.SetSelectedBackgroundColor(tcell.ColorDefault)
	app.appliedList.SetSelectedBackgroundColor(tcell.ColorDefault)

	// Highlight focused panel with green border and blue selection (lazygit style)
	switch app.currentPanelIdx {
	case 0:
		app.availableList.SetBorderColor(tcell.ColorGreen)
		app.availableList.SetSelectedBackgroundColor(selectionColor)
	case 1:
		app.appliedList.SetBorderColor(tcell.ColorGreen)
		app.appliedList.SetSelectedBackgroundColor(selectionColor)
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

func (app *App) openInEditor(filename string) {
	selected := app.getSelectedOverride()
	if selected == nil {
		return
	}

	filePath := filepath.Join(selected.FolderPath, filename)

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return
	}

	// Get editor from environment, fall back to sensible defaults
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		// Try common editors
		for _, e := range []string{"vim", "vi", "nano", "emacs"} {
			if _, err := exec.LookPath(e); err == nil {
				editor = e
				break
			}
		}
	}
	if editor == "" {
		return
	}

	// Suspend tview and run editor
	app.app.Suspend(func() {
		cmd := exec.Command(editor, filePath)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	})

	// Reload the override content after editing
	app.reloadOverride(selected.Name)
	app.updateContentAndInfo()
}

func (app *App) reloadOverride(name string) {
	for _, o := range app.overrides {
		if o.Name != name {
			continue
		}

		// Reload apply.md
		applyPath := filepath.Join(o.FolderPath, "apply.md")
		if content, err := os.ReadFile(applyPath); err == nil {
			o.ApplyInfo = string(content)

			// Re-parse frontmatter
			contentStr := string(content)
			if strings.HasPrefix(contentStr, "---") {
				parts := strings.SplitN(contentStr[3:], "---", 2)
				if len(parts) >= 1 {
					var meta struct {
						Type  string `yaml:"type"`
						Block string `yaml:"block"`
						File  string `yaml:"file"`
					}
					if err := yaml.Unmarshal([]byte(parts[0]), &meta); err == nil {
						o.Type = meta.Type
						o.Block = meta.Block
						o.File = meta.File
					}
				}
			}
		}

		// Reload override.yaml
		overridePath := filepath.Join(o.FolderPath, "override.yaml")
		if content, err := os.ReadFile(overridePath); err == nil {
			o.Content = string(content)
		}

		break
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

	// Update override string view
	overrideStr := app.buildOverrideString()
	app.overrideStringView.Clear()
	if overrideStr != "" {
		app.overrideStringView.SetText(overrideStr)
	} else {
		app.overrideStringView.SetText("(no overrides applied)")
	}

	// Update content view
	app.contentView.Clear()
	if selected == nil {
		app.contentView.SetText("Select an override to view its content")
	} else {
		content := fmt.Sprintf("[cyan::b]# %s/override.yaml[-:-:-]\n\n%s", selected.Name, highlightCode(selected.Content, "yaml"))
		if selected.ApplyInfo != "" {
			content += fmt.Sprintf("\n\n[yellow::b]# Apply Configuration[-:-:-]\n%s", highlightCode(selected.ApplyInfo, "markdown"))
		}
		app.contentView.SetText(content)
	}
}

func (app *App) updateStatusBar() {
	app.statusBar.SetText(" [1-2] panels  [space/enter] toggle  [ n ] new  [ d ] delete  [ r ] rename  [ q ] quit  [ ? ] help")
}

// modal creates a centered modal overlay that shows the background through transparent areas
func modal(content tview.Primitive, width, height int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(content, height, 0, true).
			AddItem(nil, 0, 1, false), width, 0, true).
		AddItem(nil, 0, 1, false)
}

func (app *App) showHelp() {
	app.helpOpen = true

	helpText := tview.NewTextView().
		SetDynamicColors(true).
		SetText(`[yellow::b]LazyHydra - Hydra Override Manager[-:-:-]

[green]Navigation:[-]
  1, 2            Jump to panel
  Tab / Shift+Tab Cycle panels
  h / l           Prev / Next panel
  j / k / arrows  Move cursor
  J / K           Scroll content view

[green]Actions:[-]
  Space / Enter   Apply/Remove override
  n               New override
  d               Delete override
  r               Rename override
  e               Edit apply.md
  E               Edit override.yaml
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

	app.pages.AddPage("help", modal(helpText, 60, 23), true, true)
	app.app.SetFocus(helpText)
}

func (app *App) closeHelp() {
	app.helpOpen = false
	app.pages.RemovePage("help")
	app.app.SetFocus(app.panels[app.currentPanelIdx])
	app.updateBorderColors()
}

func (app *App) showNewOverrideInput() {
	app.inputOpen = true

	inputField := tview.NewInputField().
		SetLabel("Override name: ").
		SetFieldWidth(40).
		SetFieldBackgroundColor(tcell.ColorDefault)

	inputField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			name := strings.TrimSpace(inputField.GetText())
			if name != "" {
				app.createNewOverride(name)
			}
		}
		app.closeInput()
	})

	inputField.SetBorder(true).
		SetTitle(" New Override ").
		SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.ColorGreen)

	app.pages.AddPage("input", modal(inputField, 60, 3), true, true)
	app.app.SetFocus(inputField)
}

func (app *App) closeInput() {
	app.inputOpen = false
	app.pages.RemovePage("input")
	app.app.SetFocus(app.panels[app.currentPanelIdx])
	app.updateBorderColors()
}

func (app *App) showDeleteConfirmation() {
	selected := app.getSelectedOverride()
	if selected == nil {
		return
	}

	app.deleteOpen = true

	confirmText := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter).
		SetText(fmt.Sprintf(`[yellow::b]Delete Override[-:-:-]

Are you sure you want to delete "[red]%s[-]"?

This will permanently remove the override folder.

[green]Enter[-] to confirm    [yellow]Esc/q[-] to cancel`, selected.Name))

	confirmText.SetBorder(true).
		SetTitle(" Confirm Delete ").
		SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.ColorRed)

	app.pages.AddPage("delete", modal(confirmText, 55, 11), true, true)
	app.app.SetFocus(confirmText)
}

func (app *App) closeDeleteConfirmation() {
	app.deleteOpen = false
	app.pages.RemovePage("delete")
	app.app.SetFocus(app.panels[app.currentPanelIdx])
	app.updateBorderColors()
}

func (app *App) deleteSelectedOverride() {
	selected := app.getSelectedOverride()
	if selected == nil {
		return
	}

	// Remove from applied if it was applied
	delete(app.applied, selected.Name)

	// Remove from overrides list
	for i, o := range app.overrides {
		if o.Name == selected.Name {
			app.overrides = append(app.overrides[:i], app.overrides[i+1:]...)
			break
		}
	}

	// Delete the folder from disk
	os.RemoveAll(selected.FolderPath)

	// Save state and refresh
	app.savePersistedState()
	app.refreshAll()
}

func (app *App) showRenameInput() {
	selected := app.getSelectedOverride()
	if selected == nil {
		return
	}

	app.renameOpen = true
	app.renameTarget = selected

	inputField := tview.NewInputField().
		SetLabel("New name: ").
		SetText(selected.Name).
		SetFieldWidth(40).
		SetFieldBackgroundColor(tcell.ColorDefault)

	inputField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			newName := strings.TrimSpace(inputField.GetText())
			if newName != "" && newName != app.renameTarget.Name {
				app.renameSelectedOverride(newName)
			}
		}
		app.closeRenameInput()
	})

	inputField.SetBorder(true).
		SetTitle(fmt.Sprintf(" Rename: %s ", selected.Name)).
		SetTitleAlign(tview.AlignCenter).
		SetBorderColor(tcell.ColorGreen)

	app.pages.AddPage("rename", modal(inputField, 60, 3), true, true)
	app.app.SetFocus(inputField)
}

func (app *App) closeRenameInput() {
	app.renameOpen = false
	app.renameTarget = nil
	app.pages.RemovePage("rename")
	app.app.SetFocus(app.panels[app.currentPanelIdx])
	app.updateBorderColors()
}

func (app *App) renameSelectedOverride(newName string) {
	if app.renameTarget == nil {
		return
	}

	oldName := app.renameTarget.Name
	oldPath := app.renameTarget.FolderPath
	newPath := filepath.Join(filepath.Dir(oldPath), newName)

	// Rename the folder on disk
	if err := os.Rename(oldPath, newPath); err != nil {
		return
	}

	// Update the override in memory
	app.renameTarget.Name = newName
	app.renameTarget.FolderPath = newPath

	// Update applied map if this override was applied
	if app.applied[oldName] {
		delete(app.applied, oldName)
		app.applied[newName] = true
	}

	// Re-sort overrides
	sort.Slice(app.overrides, func(i, j int) bool {
		return app.overrides[i].Name < app.overrides[j].Name
	})

	// Save state and refresh
	app.savePersistedState()
	app.refreshAll()
}

func (app *App) createNewOverride(name string) {
	dir := expandPath(app.config.OverridesDir)
	overridePath := filepath.Join(dir, name)

	// Create the folder
	if err := os.MkdirAll(overridePath, 0755); err != nil {
		return
	}

	// Create empty override.yaml
	overrideYAMLPath := filepath.Join(overridePath, "override.yaml")
	os.WriteFile(overrideYAMLPath, []byte{}, 0644)

	// Create template apply.md
	applyPath := filepath.Join(overridePath, "apply.md")
	applyContent := `---
type: ""
block: ""
---
`
	os.WriteFile(applyPath, []byte(applyContent), 0644)

	// Add the new override to the list
	override := &Override{
		Name:       name,
		Type:       "",
		Block:      "",
		FolderPath: overridePath,
		ApplyInfo:  applyContent,
	}
	app.overrides = append(app.overrides, override)

	// Re-sort overrides
	sort.Slice(app.overrides, func(i, j int) bool {
		return app.overrides[i].Name < app.overrides[j].Name
	})

	app.refreshAll()
}

