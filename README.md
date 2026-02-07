# LazyHydra

A terminal UI for managing [Hydra](https://hydra.cc/) configuration overrides, inspired by [lazygit](https://github.com/jesseduffield/lazygit).

![LazyHydra TUI](assets/lazyhydra.png)

## Features

- Browse available configuration overrides in an interactive TUI
- Apply and remove overrides with keyboard shortcuts
- Automatically symlink override configs into your Hydra config tree when applied
- Persist selections to `.envrc` for automatic environment setup via [direnv](https://direnv.net/)
- Generate override strings for Hydra CLI commands

## Installation

```bash
git clone https://github.com/yourusername/lazyhydra.git
cd lazyhydra
make clean install
```

This installs the `lazyhydra` binary to `~/.local/bin/`. Make sure this directory is in your `PATH`.

### Requirements

- Go 1.21+
- [direnv](https://direnv.net/) (for automatic environment variable loading)

## Configuration

Create a configuration file at `~/.config/lazyhydra/config.yaml`:

```yaml
# Environment variable name for storing the override string
env_var_name: HYDRA_OVERRIDES

# Directory containing your override definitions
overrides_dir: ~/.config/tbp/overrides

# Root of your Hydra config tree (symlinks are created here)
hydra_configs_dir: ~/myproject/conf

# File where state is persisted (direnv format)
project_env_file: .envrc
```

### Configuration Options

| Option | Default | Description |
|--------|---------|-------------|
| `env_var_name` | `HYDRA_OVERRIDES` | Environment variable that holds the override string |
| `overrides_dir` | `$PROJECT_ROOT/conf/overrides` | Path to directory containing override folders |
| `hydra_configs_dir` | `$PROJECT_ROOT/conf` | Root of the Hydra config tree where symlinks are created |
| `project_env_file` | `.envrc` | File for persisting state (must be in direnv format) |

**Variable substitution:**
- `~/path` expands to your home directory
- Environment variables like `$PROJECT_ROOT`, `$HOME`, etc. are expanded automatically

## Creating Overrides

Overrides are defined in folders within your `overrides_dir`. Each override folder contains two files:

```
overrides/
└── my_override/
    ├── apply.md       # Metadata and documentation
    └── override.yaml  # The actual configuration
```

### apply.md

The `apply.md` file uses YAML frontmatter to define how the override is applied:

```markdown
---
type: "+"
block: "experiment.config.logging"
---

Optional documentation about what this override does.
```

**Frontmatter fields:**

| Field | Description |
|-------|-------------|
| `type` | `"+"` for merge or `"="` for replace. For value overrides (no `block`), use `"++"` or `"--"`. |
| `block` | The Hydra config group path where this override applies (e.g., `experiment.config.logging`). Omit for value overrides. |

When an override with a `block` is applied, LazyHydra creates a symlink from `override.yaml` into your Hydra config tree at `hydra_configs_dir/<block_as_path>/<name>_override.yaml`. For example, applying an override named `detailed_logging` with block `experiment.config.logging` creates:

```
<hydra_configs_dir>/experiment/config/logging/detailed_logging_override.yaml -> <overrides_dir>/detailed_logging/override.yaml
```

And generates the override string: `+experiment/config/logging=detailed_logging_override`

#### Value overrides

If `block` is omitted, the override is treated as a value override. The keys in `override.yaml` are flattened into `key=value` pairs:

```markdown
---
type: "++"
---
```

With an `override.yaml` of `{episodes: 3, model.hidden_size: 256}`, this generates: `++episodes=3 ++model.hidden_size=256`

### override.yaml

The `override.yaml` file contains the actual configuration values:

```yaml
log_level: DEBUG
log_to_file: true
log_to_stderr: true
```

### Example

To create an override that enables detailed logging:

**overrides/detailed_logging/apply.md:**
```markdown
---
type: "+"
block: "experiment.config.logging"
---

Enables detailed logging with debug output.
```

**overrides/detailed_logging/override.yaml:**
```yaml
log_level: DEBUG
log_to_file: true
log_to_stderr: true
```

When applied, this symlinks `override.yaml` into `<hydra_configs_dir>/experiment/config/logging/detailed_logging_override.yaml` and adds `+experiment/config/logging=detailed_logging_override` to the override string.

## Usage

### Interactive TUI

Launch the interactive interface:

```bash
lazyhydra
```

### Keybindings

| Key | Action |
|-----|--------|
| `1` `2` | Jump to panel |
| `Tab` / `Shift+Tab` | Cycle panels |
| `h` / `l` | Previous / Next panel |
| `j` / `k` | Move down / up |
| `J` / `K` | Scroll content view |
| `Space` / `Enter` | Toggle override (apply or remove) |
| `n` | Create new override |
| `d` | Duplicate override (creates `[name]_copy`) |
| `D` | Delete override (with confirmation) |
| `r` | Rename override |
| `e` | Edit `apply.md` in `$EDITOR` |
| `E` | Edit `override.yaml` in `$EDITOR` |
| `y` | Copy selected override string to clipboard |
| `Y` | Copy all applied override strings to clipboard |
| `?` | Show help |
| `q` / `Esc` | Quit |

### CLI Modes

```bash
lazyhydra           # Launch interactive TUI
lazyhydra -l        # List all overrides and their status
lazyhydra -p        # Print the current override string
lazyhydra -h        # Show help
```

### Using with Hydra

After selecting overrides in LazyHydra, the override string is stored in your `.envrc`. You can use it in your Hydra commands:

```bash
# The HYDRA_OVERRIDES variable is automatically set by direnv
python train.py $HYDRA_OVERRIDES_STR
```

Or print it directly for use in scripts:

```bash
python train.py $(lazyhydra -p)
```
