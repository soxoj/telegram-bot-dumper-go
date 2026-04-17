package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"golang.org/x/net/proxy"
)

const configFileName = "config.json"

// UserAuth handles user account authentication for trap mode
type UserAuth struct {
	client  *telegram.Client
	api     *tg.Client
	phone   string
	apiID   int
	apiHash string
	useTor  bool
}

// UserAppConfig holds the user's Telegram app credentials
type UserAppConfig struct {
	Phone   string `json:"phone"`
	ApiID   int    `json:"api_id"`
	ApiHash string `json:"api_hash"`
}

// configFilePath returns the path to the persistent config file.
// Stored next to the binary so it travels with the tool.
func configFilePath() string {
	exe, err := os.Executable()
	if err != nil {
		return configFileName
	}
	return filepath.Join(filepath.Dir(exe), configFileName)
}

// LoadOrCreateAppConfig loads config.json if it exists, otherwise
// walks the user through an interactive setup and saves it.
func LoadOrCreateAppConfig() (*UserAppConfig, error) {
	path := configFilePath()

	// Try loading existing
	if data, err := os.ReadFile(path); err == nil {
		cfg := &UserAppConfig{}
		if err := json.Unmarshal(data, cfg); err == nil && cfg.ApiID != 0 && cfg.ApiHash != "" {
			fmt.Printf("[config] Loaded credentials from %s\n", path)
			return cfg, nil
		}
		// File exists but is corrupt / incomplete — fall through to setup
	}

	// Interactive first-run setup
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║              FIRST-RUN SETUP                            ║")
	fmt.Println("║                                                         ║")
	fmt.Println("║  Register a Telegram app at https://my.telegram.org     ║")
	fmt.Println("║  to get your API ID and API Hash.                       ║")
	fmt.Println("║                                                         ║")
	fmt.Println("║  These will be saved to config.json so you only         ║")
	fmt.Println("║  have to do this once.                                  ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	cfg := &UserAppConfig{}

	// API ID
	for cfg.ApiID == 0 {
		fmt.Print("  API ID: ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		fmt.Sscanf(line, "%d", &cfg.ApiID)
		if cfg.ApiID == 0 {
			fmt.Println("  ✗ Must be a number. Try again.")
		}
	}

	// API Hash
	for cfg.ApiHash == "" {
		fmt.Print("  API Hash: ")
		line, _ := reader.ReadString('\n')
		cfg.ApiHash = strings.TrimSpace(line)
		if cfg.ApiHash == "" {
			fmt.Println("  ✗ Cannot be empty. Try again.")
		}
	}

	// Phone (optional — only needed for trap mode, but grab it now so we never ask again)
	fmt.Print("  Phone number (with country code, e.g. 14155551234): ")
	line, _ := reader.ReadString('\n')
	cfg.Phone = strings.TrimSpace(line)

	// Save
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return nil, fmt.Errorf("failed to write %s: %w", path, err)
	}

	fmt.Printf("\n  ✓ Saved to %s\n\n", path)
	return cfg, nil
}

// LoadUserAppConfig loads credentials from a specific file path (legacy support).
// Accepts both the old key=value format and the new JSON format.
func LoadUserAppConfig(path string) (*UserAppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read app config file: %w", err)
	}

	// Try JSON first
	cfg := &UserAppConfig{}
	if err := json.Unmarshal(data, cfg); err == nil && cfg.ApiID != 0 && cfg.ApiHash != "" {
		return cfg, nil
	}

	// Fall back to key=value format
	cfg = &UserAppConfig{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "phone":
			cfg.Phone = val
		case "api_id":
			fmt.Sscanf(val, "%d", &cfg.ApiID)
		case "api_hash":
			cfg.ApiHash = val
		}
	}

	if cfg.ApiID == 0 || cfg.ApiHash == "" {
		return nil, fmt.Errorf("incomplete app config: need at least api_id and api_hash")
	}

	return cfg, nil
}

// terminalAuth implements auth.UserAuthenticator with interactive terminal prompts
type terminalAuth struct {
	phone string
}

func (t terminalAuth) Phone(_ context.Context) (string, error) {
	if t.phone != "" {
		return t.phone, nil
	}
	// Phone wasn't in config — ask now
	fmt.Print("Enter your phone number (with country code): ")
	reader := bufio.NewReader(os.Stdin)
	phone, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read phone: %w", err)
	}
	return strings.TrimSpace(phone), nil
}

func (t terminalAuth) Code(_ context.Context, _ *tg.AuthSentCode) (string, error) {
	fmt.Print("Enter the code sent to your Telegram: ")
	reader := bufio.NewReader(os.Stdin)
	code, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read code: %w", err)
	}
	return strings.TrimSpace(code), nil
}

func (t terminalAuth) AcceptTermsOfService(_ context.Context, _ tg.HelpTermsOfService) error {
	return nil
}

func (t terminalAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("sign up not supported — this account must already exist")
}

func (t terminalAuth) Password(_ context.Context) (string, error) {
	fmt.Print("Enter your 2FA password: ")
	reader := bufio.NewReader(os.Stdin)
	pass, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read password: %w", err)
	}
	return strings.TrimSpace(pass), nil
}

// NewUserAuth creates a new UserAuth instance
func NewUserAuth(appCfg *UserAppConfig, useTor bool) *UserAuth {
	return &UserAuth{
		phone:   appCfg.Phone,
		apiID:   appCfg.ApiID,
		apiHash: appCfg.ApiHash,
		useTor:  useTor,
	}
}

// CreateClient creates the Telegram client for the user account
func (ua *UserAuth) CreateClient() *telegram.Client {
	opts := telegram.Options{
		// Persistent session — only need OTP/2FA on first run
		SessionStorage: &telegram.FileSessionStorage{
			Path: "user_session.json",
		},
	}

	if ua.useTor {
		socks5Dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:9050", nil, proxy.Direct)
		if err != nil {
			fmt.Printf("[USER-AUTH] Warning: failed to create Tor proxy: %v\n", err)
		} else {
			contextDialer, ok := socks5Dialer.(proxy.ContextDialer)
			if ok {
				opts.Resolver = dcs.Plain(dcs.PlainOptions{
					Dial: contextDialer.DialContext,
				})
				fmt.Println("[USER-AUTH] Tor SOCKS5 proxy enabled")
			}
		}
	}

	ua.client = telegram.NewClient(ua.apiID, ua.apiHash, opts)
	return ua.client
}

// Authenticate handles the phone-based auth flow.
// On first run: prompts for OTP code (and 2FA password if enabled).
// On subsequent runs: session file is reused, no prompts.
func (ua *UserAuth) Authenticate(ctx context.Context) (*tg.Client, error) {
	flow := auth.NewFlow(
		terminalAuth{phone: ua.phone},
		auth.SendCodeOptions{},
	)

	if err := ua.client.Auth().IfNecessary(ctx, flow); err != nil {
		return nil, fmt.Errorf("user auth failed: %w", err)
	}

	ua.api = tg.NewClient(ua.client)
	fmt.Println("[USER-AUTH] Authenticated ✓")

	return ua.api, nil
}

// GetAPI returns the authenticated tg.Client
func (ua *UserAuth) GetAPI() *tg.Client {
	return ua.api
}

// GetTelegramClient returns the underlying telegram.Client
func (ua *UserAuth) GetTelegramClient() *telegram.Client {
	return ua.client
}
