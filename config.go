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
	// Loaded from config.json (auto-created on first run)
	ApiID   int
	ApiHash string
	Phone   string // only needed for trap mode

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
	flag.Usage = customUsage

	token := flag.String("token", "", "Bot token (prompted interactively if omitted)")
	listenOnly := flag.Bool("listen-only", false, "Only listen for new messages, don't dump history")
	lookahead := flag.Int("lookahead", 0, "Additional cycles to check for empty messages")
	useTor := flag.Bool("tor", false, "Enable Tor SOCKS5 proxy (127.0.0.1:9050)")
	excludeExts := flag.String("exclude-exts", "", "Comma-separated extensions to skip (e.g. pdf,php,txt)")
	excludeExtsFile := flag.String("exclude-exts-file", "", "File with excluded extensions (one per line)")
	outputFile := flag.String("output-file", "", "Append new messages to this file")
	flag.Parse()

	// Load or create persistent config (api_id, api_hash, phone)
	appCfg, err := LoadOrCreateAppConfig()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		ApiID:           appCfg.ApiID,
		ApiHash:         appCfg.ApiHash,
		Phone:           appCfg.Phone,
		Token:           *token,
		ListenOnly:      *listenOnly,
		Lookahead:       *lookahead,
		UseTor:          *useTor,
		ExcludeExtsFile: *excludeExtsFile,
		OutputFile:      *outputFile,
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if err := cfg.loadExcludedExts(*excludeExts); err != nil {
		return nil, err
	}
	if err := cfg.setupOutputFile(); err != nil {
		return nil, err
	}
	if err := cfg.ensureToken(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validate checks that all required fields are present
func (c *Config) validate() error {
	if c.ApiID == 0 || c.ApiHash == "" {
		return fmt.Errorf("api_id and api_hash are required — delete config.json and re-run to set them up")
	}
	return nil
}

// loadExcludedExts loads excluded extensions from string or file
func (c *Config) loadExcludedExts(excludeExts string) error {
	if excludeExts != "" && c.ExcludeExtsFile != "" {
		return fmt.Errorf("--exclude-exts and --exclude-exts-file cannot be used together")
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

// ensureToken makes sure we have at least one bot token
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

// Close cleans up open handles
func (c *Config) Close() error {
	if c.OutputFileHandle != nil {
		return c.OutputFileHandle.Close()
	}
	return nil
}

// customUsage prints usage with double-dash flags
func customUsage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\n  Credentials (api_id, api_hash, phone) are stored in config.json.\n")
	fmt.Fprintf(os.Stderr, "  On first run you'll be prompted to set them up.\n\n")
	flag.VisitAll(func(f *flag.Flag) {
		var typeStr string
		defValue := f.DefValue
		if defValue == "false" || defValue == "true" {
			typeStr = ""
		} else if defValue == "0" {
			typeStr = " int"
		} else if defValue == "" {
			typeStr = " string"
		} else {
			typeStr = " value"
		}
		if defValue == "" || defValue == "false" || defValue == "0" {
			fmt.Fprintf(os.Stderr, "  --%s%s\n    \t%s\n", f.Name, typeStr, f.Usage)
		} else {
			fmt.Fprintf(os.Stderr, "  --%s%s\n    \t%s (default: %s)\n", f.Name, typeStr, f.Usage, defValue)
		}
	})
}

// parseExcludedExtsFromString parses comma-separated extensions into a set
func parseExcludedExtsFromString(exts string) (map[string]bool, error) {
	result := make(map[string]bool)
	if exts == "" {
		return result, nil
	}
	for _, part := range strings.Split(exts, ",") {
		ext := strings.TrimSpace(part)
		if ext != "" {
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
			ext = strings.ToLower(strings.TrimPrefix(ext, "."))
			result[ext] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return result, nil
}
