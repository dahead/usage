# Directory Usage Analyzer

A simple terminal tool to analyze directory sizes, written in Go.

## Features

- Shows size and percentage for each directory/file
- Keyboard navigation

## Controls

- `↑/↓` - Navigate
- `Enter` - Enter directory
- `Backspace` - Go back
- `q` - Quit


## Dependencies

- [bubbletea](https://github.com/charmbracelet/bubbletea)
- [lipgloss](https://github.com/charmbracelet/lipgloss)
- [go-humanize](https://github.com/dustin/go-humanize)


## Installation and Usage

```bash
# Build the tool
go build -o usage .

# Run with directories only (default)
./usage

# Run with files included
USAGE_SHOW_FILES=1 ./usage
```