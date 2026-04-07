package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

// Config holds all configuration for the application
type Config struct {
	ApiID           int
	ApiHash         string
	Token           string
	ListenOnly      bool
	Lookahead       int
	UseTor          bool
	ExcludeExts     map[string]bool
	ExcludeExtsFile string
	OutputFile      string
	OutputFileHandle *os.File
}

// LoadConfig parses command line flags and returns a validated Config
func LoadConfig() (*Config, error) {
	// Override flag.Usage to show flags with double dashes
	flag.Usage = customUsage
	
	// Parse command line arguments
	apiID := flag.Int("api-id", 0, "Telegram API ID (required)")
	apiHash := flag.String("api-hash", "", "Telegram API Hash (required)")
	token := flag.String("token", "", "Bot token (can be entered interactively if not provided)")
	listenOnly := flag.Bool("listen-only", false, "Only listen for new messages, don't dump history")
	lookahead := flag.Int("lookahead", 0, "Number of additional cycles to skip empty messages")
	useTor := flag.Bool("tor", false, "Enable Tor SOCKS5 proxy (127.0.0.1:9050)")
	excludeExts := flag.String("exclude-exts", "", "Comma-separated list of file extensions to exclude (e.g., pdf,php,txt)")
	excludeExtsFile := flag.String("exclude-exts-file", "", "Path to file with excluded extensions (one per line)")
	outputFile := flag.String("output-file", "", "Path to file where new messages output will be saved")
	flag.Parse()

	cfg := &Config{
		ApiID:           *apiID,
		ApiHash:         *apiHash,
		Token:           *token,
		ListenOnly:      *listenOnly,
		Lookahead:       *lookahead,
		UseTor:          *useTor,
		ExcludeExtsFile: *excludeExtsFile,
		OutputFile:      *outputFile,
	}

	// Validate required arguments
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Load excluded extensions
	if err := cfg.loadExcludedExts(*excludeExts); err != nil {
		return nil, err
	}

	// Setup output file for new messages if specified
	if err := cfg.setupOutputFile(); err != nil {
		return nil, err
	}

	// Get bot token interactively if not provided
	if err := cfg.ensureToken(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate validates required configuration fields
func (c *Config) Validate() error {
	if c.ApiID == 0 || c.ApiHash == "" {
		flag.Usage()
		return fmt.Errorf("--api-id and --api-hash are required")
	}

	return nil
}

// loadExcludedExts loads excluded extensions from string or file
func (c *Config) loadExcludedExts(excludeExts string) error {
	// Validate that exclude-exts and exclude-exts-file are not used simultaneously
	if excludeExts != "" && c.ExcludeExtsFile != "" {
		return fmt.Errorf("--exclude-exts and --exclude-exts-file cannot be used simultaneously")
	}

	var err error
	if excludeExts != "" {
		c.ExcludeExts, err = parseExcludedExtsFromString(excludeExts)
		if err != nil {
			return fmt.Errorf("failed to parse --exclude-exts: %w", err)
		}
	} else if c.ExcludeExtsFile != "" {
		c.ExcludeExts, err = parseExcludedExtsFromFile(c.ExcludeExtsFile)
		if err != nil {
			return fmt.Errorf("failed to parse --exclude-exts-file: %w", err)
		}
	}

	return nil
}

// setupOutputFile opens the output file if specified
func (c *Config) setupOutputFile() error {
	if c.OutputFile == "" {
		return nil
	}

	var err error
	c.OutputFileHandle, err = os.OpenFile(c.OutputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open output file: %w", err)
	}

	return nil
}

// ensureToken ensures bot token is set, prompting interactively if needed
func (c *Config) ensureToken() error {
	if c.Token != "" {
		return nil
	}

	fmt.Print("Enter bot token: ")
	fmt.Scanln(&c.Token)
	if c.Token == "" {
		return fmt.Errorf("bot token is required")
	}

	return nil
}

// Close closes any open file handles
func (c *Config) Close() error {
	if c.OutputFileHandle != nil {
		return c.OutputFileHandle.Close()
	}
	return nil
}

// customUsage prints usage information with double dashes for flags
func customUsage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	flag.VisitAll(func(f *flag.Flag) {
		// Determine type from flag name patterns or default value
		var typeStr string
		defValue := f.DefValue
		
		// Check if it's a bool flag (default is "false" or "true")
		if defValue == "false" || defValue == "true" {
			typeStr = ""
		} else if defValue == "0" {
			// Could be int, check if name suggests it
			typeStr = " int"
		} else if defValue == "" {
			// Empty default, likely string
			typeStr = " string"
		} else {
			// Try to determine from usage or name
			typeStr = " value"
		}
		
		// Show flag with type, but don't show default for empty strings, false, or 0
		if defValue == "" || defValue == "false" || defValue == "0" {
			fmt.Fprintf(os.Stderr, "  --%s%s\n    \t%s\n", f.Name, typeStr, f.Usage)
		} else {
			fmt.Fprintf(os.Stderr, "  --%s%s\n    \t%s (default: %s)\n", f.Name, typeStr, f.Usage, defValue)
		}
	})
}

// parseExcludedExtsFromString parses comma-separated extensions string into a map
func parseExcludedExtsFromString(exts string) (map[string]bool, error) {
	result := make(map[string]bool)
	if exts == "" {
		return result, nil
	}
	
	parts := strings.Split(exts, ",")
	for _, part := range parts {
		ext := strings.TrimSpace(part)
		if ext != "" {
			// Normalize: remove leading dot if present, convert to lowercase
			ext = strings.ToLower(strings.TrimPrefix(ext, "."))
			result[ext] = true
		}
	}
	return result, nil
}

// parseExcludedExtsFromFile reads excluded extensions from a file (one per line)
func parseExcludedExtsFromFile(filePath string) (map[string]bool, error) {
	result := make(map[string]bool)
	
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		ext := strings.TrimSpace(scanner.Text())
		if ext != "" {
			// Normalize: remove leading dot if present, convert to lowercase
			ext = strings.ToLower(strings.TrimPrefix(ext, "."))
			result[ext] = true
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return result, nil
}
