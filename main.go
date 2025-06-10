package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
)

// Global cache for directory sizes
var (
	sizeCache  = make(map[string]int64)
	cacheMutex sync.RWMutex
)

// DirEntry represents a directory with its size and children
type DirEntry struct {
	Name      string
	Path      string
	Size      int64
	Percent   float64
	Children  []*DirEntry
	IsDir     bool
	Level     int
	ParentDir *DirEntry
}

// LoadingMsg is sent when loading starts
type LoadingMsg struct {
	Path string
}

// LoadingCompleteMsg is sent when loading completes
type LoadingCompleteMsg struct {
	Dir   *DirEntry
	Error error
}

// SpinnerMsg for spinner animation
type SpinnerMsg time.Time

// Model represents the application state
type Model struct {
	RootDir     *DirEntry
	CursorPos   int
	ScrollPos   int
	VisibleDirs []*DirEntry
	Error       error
	Loading     bool
	LoadingPath string
	SpinnerIdx  int
	ShowFiles   bool
	Height      int
}

// ExecuteFileMsg is sent when file execution completes
type ExecuteFileMsg struct {
	FilePath string
	Success  bool
	Error    error
}

var spinnerFrames = []string{"◐", "◓", "◑", "◒"}

func (m Model) Init() tea.Cmd {
	showFiles := os.Getenv("USAGE_SHOW_FILES") != "false"
	m.ShowFiles = showFiles
	m.Height = 20 // Default height, will be updated when we get window size
	return tea.Batch(m.doSpinner(), tea.EnterAltScreen)
}

func (m Model) doSpinner() tea.Cmd {
	return tea.Tick(time.Millisecond*100, func(t time.Time) tea.Msg {
		return SpinnerMsg(t)
	})
}

func (m Model) loadDirectory(path string) tea.Cmd {
	return func() tea.Msg {
		dir, err := scanDirectoryWithCache(path, nil, 0, m.ShowFiles)
		if err != nil {
			return LoadingCompleteMsg{nil, err}
		}
		dir.Percent = 100.0
		return LoadingCompleteMsg{dir, nil}
	}
}

// getCachedSize returns cached size or calculates it once
func getCachedSize(path string) int64 {
	cacheMutex.RLock()
	if size, exists := sizeCache[path]; exists {
		cacheMutex.RUnlock()
		return size
	}
	cacheMutex.RUnlock()

	// Calculate size with full recursion (but only once)
	size := calculateFullDirSize(path)

	cacheMutex.Lock()
	sizeCache[path] = size
	cacheMutex.Unlock()

	return size
}

// calculateFullDirSize does full recursive calculation
func calculateFullDirSize(path string) int64 {
	var size int64

	entries, err := os.ReadDir(path)
	if err != nil {
		return 0
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		childPath := filepath.Join(path, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.IsDir() {
			size += calculateFullDirSize(childPath) // Recursive call
		} else {
			size += info.Size()
		}
	}

	return size
}

func (m *Model) ensureCursorVisible() {
	if len(m.VisibleDirs) == 0 {
		return
	}

	// Calculate visible window
	maxVisible := m.Height - 2 // Leave space for header

	// Adjust scroll position to keep cursor visible
	if m.CursorPos < m.ScrollPos {
		m.ScrollPos = m.CursorPos
	} else if m.CursorPos >= m.ScrollPos+maxVisible {
		m.ScrollPos = m.CursorPos - maxVisible + 1
	}

	// Ensure scroll position is within bounds
	if m.ScrollPos < 0 {
		m.ScrollPos = 0
	}
	maxScroll := len(m.VisibleDirs) - maxVisible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.ScrollPos > maxScroll {
		m.ScrollPos = maxScroll
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Height = msg.Height
		m.ensureCursorVisible()
		return m, nil

	case LoadingMsg:
		m.Loading = true
		m.LoadingPath = msg.Path
		return m, m.loadDirectory(msg.Path)

	case LoadingCompleteMsg:
		m.Loading = false
		if msg.Error != nil {
			m.Error = msg.Error
		} else {
			m.RootDir = msg.Dir
			m.updateVisibleDirs()
			// Ensure first entry is always marked after loading
			m.CursorPos = 0
			m.ScrollPos = 0
			m.ensureCursorVisible()
		}
		return m, nil

	case SpinnerMsg:
		if m.Loading {
			m.SpinnerIdx = (m.SpinnerIdx + 1) % len(spinnerFrames)
			return m, m.doSpinner()
		}
		return m, nil

	case tea.KeyMsg:
		if m.Loading {
			if msg.String() == "q" || msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.CursorPos > 0 {
				m.CursorPos--
				m.ensureCursorVisible()
			}
		case "down", "j":
			if m.CursorPos < len(m.VisibleDirs)-1 {
				m.CursorPos++
				m.ensureCursorVisible()
			}

		case "enter":
			if m.CursorPos < len(m.VisibleDirs) {
				dir := m.VisibleDirs[m.CursorPos]
				if dir.IsDir {
					if dir.Name == ".." {
						parentPath := filepath.Dir(m.RootDir.Path)
						if parentPath != m.RootDir.Path {
							return m, func() tea.Msg {
								return LoadingMsg{Path: parentPath}
							}
						}
					} else {
						return m, func() tea.Msg {
							return LoadingMsg{Path: dir.Path}
						}
					}
				} else {
					// Execute file
					return m, m.executeFile(dir.Path)
				}
			}
		case "backspace", "h":
			parentPath := filepath.Dir(m.RootDir.Path)
			if parentPath != m.RootDir.Path {
				return m, func() tea.Msg {
					return LoadingMsg{Path: parentPath}
				}
			}
		case "home", "g":
			m.CursorPos = 0
			m.ensureCursorVisible()
		case "end", "G":
			if len(m.VisibleDirs) > 0 {
				m.CursorPos = len(m.VisibleDirs) - 1
				m.ensureCursorVisible()
			}
		case "pgup":
			maxVisible := m.Height - 2
			m.CursorPos -= maxVisible
			if m.CursorPos < 0 {
				m.CursorPos = 0
			}
			m.ensureCursorVisible()
		case "pgdown":
			maxVisible := m.Height - 2
			m.CursorPos += maxVisible
			if m.CursorPos >= len(m.VisibleDirs) {
				m.CursorPos = len(m.VisibleDirs) - 1
			}
			m.ensureCursorVisible()
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.Error != nil {
		return fmt.Sprintf("Error: %v", m.Error)
	}

	if m.Loading {
		spinner := spinnerFrames[m.SpinnerIdx]
		return fmt.Sprintf("%s Loading %s...", spinner, m.LoadingPath)
	}

	var s strings.Builder

	// Header with current path
	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("226")). // Bright yellow text
		Background(lipgloss.Color("235")). // Dark gray background
		AlignHorizontal(lipgloss.Right)
	s.WriteString(headerStyle.Render(fmt.Sprint(m.RootDir.Path)) + "\n")

	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("240"))
	dirStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	fileStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	sizeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("248"))
	percentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

	// Calculate visible window
	maxVisible := m.Height - 2 // Leave space for header
	start := m.ScrollPos
	end := start + maxVisible
	if end > len(m.VisibleDirs) {
		end = len(m.VisibleDirs)
	}

	// Show only visible entries
	for i := start; i < end; i++ {
		dir := m.VisibleDirs[i]

		// Add indentation for hierarchy
		indent := strings.Repeat("  ", dir.Level)

		// Add prefix for directory/file type
		var prefix string
		if dir.IsDir {
			prefix = "▶ "
		} else {
			prefix = "· "
		}

		name := dir.Name
		if len(name) > 50 {
			name = name[:47] + "..."
		}

		if dir.IsDir {
			name = dirStyle.Render(name + "/")
		} else {
			name = fileStyle.Render(name)
		}

		size := sizeStyle.Render(fmt.Sprintf("%10s", humanize.Bytes(uint64(dir.Size))))
		percent := percentStyle.Render(fmt.Sprintf("%7.1f%%", dir.Percent))

		// Build the line with proper indentation and column alignment
		var line string
		if i == m.CursorPos {
			// For selected line, add selection indicator but maintain column alignment
			line = fmt.Sprintf("> %s%s%-70s%s%s", indent, prefix, name, size, percent)
			line = selectedStyle.Render(line)
		} else {
			// For non-selected lines, add 2 spaces to match the "> " width
			line = fmt.Sprintf("  %s%s%-70s%s%s", indent, prefix, name, size, percent)
		}

		s.WriteString(line + "\n")
	}

	return s.String()
}

func (m *Model) updateVisibleDirs() {
	m.VisibleDirs = []*DirEntry{}

	parentPath := filepath.Dir(m.RootDir.Path)
	if parentPath != m.RootDir.Path {
		parentEntry := &DirEntry{
			Name:  "..",
			Path:  parentPath,
			IsDir: true,
			Level: 0,
		}
		m.VisibleDirs = append(m.VisibleDirs, parentEntry)
	}

	for _, child := range m.RootDir.Children {
		if child.IsDir || m.ShowFiles {
			m.VisibleDirs = append(m.VisibleDirs, child)
		}
	}

	m.CursorPos = 0
	m.ScrollPos = 0
}

// scanDirectoryWithCache scans directory using cached sizes when possible
func scanDirectoryWithCache(path string, parentDir *DirEntry, level int, showFiles bool) (*DirEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	entry := &DirEntry{
		Name:      filepath.Base(path),
		Path:      path,
		IsDir:     info.IsDir(),
		Level:     level,
		ParentDir: parentDir,
	}

	if !info.IsDir() {
		entry.Size = info.Size()
		return entry, nil
	}

	// For directories, scan immediate children only
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	var totalSize int64
	var directories []*DirEntry
	var files []*DirEntry

	for _, e := range entries {
		childPath := filepath.Join(path, e.Name())

		if strings.HasPrefix(e.Name(), ".") {
			continue
		}

		childInfo, err := e.Info()
		if err != nil {
			continue
		}

		if childInfo.IsDir() {
			// Use cached size (calculated with full recursion when first needed)
			childSize := getCachedSize(childPath)

			child := &DirEntry{
				Name:      e.Name(),
				Path:      childPath,
				Size:      childSize,
				IsDir:     true,
				Level:     level + 1,
				ParentDir: entry,
			}
			directories = append(directories, child)
			totalSize += childSize
		} else if showFiles {
			child := &DirEntry{
				Name:      e.Name(),
				Path:      childPath,
				Size:      childInfo.Size(),
				IsDir:     false,
				Level:     level + 1,
				ParentDir: entry,
			}
			files = append(files, child)
			totalSize += childInfo.Size()
		} else {
			totalSize += childInfo.Size()
		}
	}

	// Sort directories by size (descending)
	sort.Slice(directories, func(i, j int) bool {
		return directories[i].Size > directories[j].Size
	})

	// Sort files by size (descending)
	sort.Slice(files, func(i, j int) bool {
		return files[i].Size > files[j].Size
	})

	entry.Children = append(entry.Children, directories...)
	entry.Children = append(entry.Children, files...)

	entry.Size = totalSize

	// Calculate percentages
	if totalSize > 0 {
		for _, child := range entry.Children {
			child.Percent = float64(child.Size) / float64(totalSize) * 100
		}
	}

	return entry, nil
}

func (m Model) executeFile(filePath string) tea.Cmd {
	return func() tea.Msg {
		// Determine how to execute the file based on its extension or executable bit
		var cmd *exec.Cmd

		// Check if file is executable
		info, err := os.Stat(filePath)
		if err != nil {
			return ExecuteFileMsg{filePath, false, err}
		}

		// On Unix-like systems, check if file has execute permission
		if info.Mode()&0111 != 0 {
			// File is executable, run it directly
			cmd = exec.Command(filePath)
		} else {
			cmd = exec.Command("xdg-open", filePath)
		}

		// Set working directory to the file's directory
		cmd.Dir = filepath.Dir(filePath)

		// Execute the command
		err = cmd.Start()
		if err != nil {
			return ExecuteFileMsg{filePath, false, err}
		}

		return ExecuteFileMsg{filePath, true, nil}
	}
}

// printIntegrationCommand prints the export command to add this app to PATH
func printIntegrationCommand() {
	execPath, err := os.Executable()
	if err != nil {
		fmt.Printf("Error getting executable path: %v\n", err)
		return
	}

	// Get the absolute path
	absPath, err := filepath.Abs(execPath)
	if err != nil {
		fmt.Printf("Error getting absolute path: %v\n", err)
		return
	}

	// Get the directory containing the executable
	binDir := filepath.Dir(absPath)

	fmt.Printf("To add this application to your PATH, run the following command:\n\n")
	fmt.Printf("export PATH=\"%s:$PATH\"\n\n", binDir)
	fmt.Printf("To make this permanent, add the above line to your shell profile:\n")
	fmt.Printf("  ~/.bashrc (for bash)\n")
	fmt.Printf("  ~/.zshrc (for zsh)\n")
	fmt.Printf("  ~/.profile (for general use)\n\n")
	fmt.Printf("Example:\n")
	fmt.Printf("echo 'export PATH=\"%s:$PATH\"' >> ~/.bashrc\n", binDir)
}

func main() {
	currentDir, err := os.Getwd()
	if err != nil {
		fmt.Printf("Error getting current directory: %v\n", err)
		os.Exit(1)
	}

	// Check for integration command
	if len(os.Args) > 1 && os.Args[1] == "/INTEGRATE" {
		printIntegrationCommand()
		return
	}

	// get options
	showFiles := os.Getenv("USAGE_SHOW_FILES") != "false"

	rootDir, err := scanDirectoryWithCache(currentDir, nil, 0, showFiles)
	if err != nil {
		fmt.Printf("Error scanning directory: %v\n", err)
		os.Exit(1)
	}

	rootDir.Percent = 100.0

	model := Model{
		RootDir:   rootDir,
		ShowFiles: showFiles,
		Error:     nil,
		CursorPos: 0,
		ScrollPos: 0,
		Height:    20,
	}
	model.updateVisibleDirs()

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v\n", err)
		os.Exit(1)
	}
}
