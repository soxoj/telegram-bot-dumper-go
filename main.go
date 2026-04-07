package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"golang.org/x/net/proxy"
)

const VERSION = "v3-2026-01-22-13-10"

func main() {
	// Print version and executable path for debugging
	exePath, _ := os.Executable()
	fmt.Printf("=== DUMPER VERSION: %s ===\n", VERSION)
	fmt.Printf("=== EXECUTABLE: %s ===\n", exePath)

	// Load configuration
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer cfg.Close()

	// Extract bot ID from token
	botIDStr := strings.Split(cfg.Token, ":")[0]
	if botIDStr == "" {
		fmt.Fprintf(os.Stderr, "Error: invalid bot token format\n")
		os.Exit(1)
	}

	botID, err := strconv.ParseInt(botIDStr, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse bot ID: %v\n", err)
		os.Exit(1)
	}

	// Setup base path
	basePath := botIDStr
	if _, err := os.Stat(basePath); err == nil {
		// Directory exists, rename it with timestamp
		newPath := fmt.Sprintf("%s_%d", basePath, time.Now().Unix())
		if err := os.Rename(basePath, newPath); err != nil {
			fmt.Printf("Warning: failed to rename existing directory: %v\n", err)
		} else {
			fmt.Printf("Renamed existing directory to %s\n", newPath)
			// Copy session file if it exists
			sessionFile := fmt.Sprintf("%s/%s.session", newPath, basePath)
			if _, err := os.Stat(sessionFile); err == nil {
				newSessionFile := fmt.Sprintf("%s/%s.session", basePath, basePath)
				if err := copyFile(sessionFile, newSessionFile); err != nil {
					fmt.Printf("Warning: failed to copy session file: %v\n", err)
				}
			}
		}
	}
	if err := os.MkdirAll(basePath, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create base directory: %v\n", err)
		os.Exit(1)
	}

	// Create update dispatcher
	dispatcher := tg.NewUpdateDispatcher()

	// Setup Telegram client options
	opts := telegram.Options{
		UpdateHandler: dispatcher,
	}

	// Setup proxy if Tor is enabled
	if cfg.UseTor {
		socks5Dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:9050", nil, proxy.Direct)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create Tor proxy: %v\n", err)
			os.Exit(1)
		}
		// Cast to ContextDialer to use DialContext method
		contextDialer, ok := socks5Dialer.(proxy.ContextDialer)
		if !ok {
			fmt.Fprintf(os.Stderr, "Error: SOCKS5 dialer does not support ContextDialer interface\n")
			os.Exit(1)
		}
		opts.Resolver = dcs.Plain(dcs.PlainOptions{
			Dial: contextDialer.DialContext,
		})
		fmt.Println("Tor SOCKS5 proxy enabled (127.0.0.1:9050)")
	}

	// Create Telegram client
	client := telegram.NewClient(cfg.ApiID, cfg.ApiHash, opts)

	// Run the bot
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		cancel()
	}()

	if err := client.Run(ctx, func(ctx context.Context) error {
		// Authenticate bot
		_, err := client.Auth().Bot(ctx, cfg.Token)
		if err != nil {
			return fmt.Errorf("failed to authenticate bot: %w", err)
		}

		// Get bot info
		api := client.API()
		me, err := api.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		if err != nil {
			return fmt.Errorf("failed to get bot info: %w", err)
		}

		botUser := me.Users[0].(*tg.User)
		fmt.Printf("Authenticated as bot: %s (ID: %d)\n", botUser.FirstName, botUser.ID)

		// Print bot info
		dumper := &Dumper{} // Temporary, will be initialized properly
		dumper.PrintBotInfo(botUser)

		// Save bot info
		storage := NewStorage(basePath)
		botInfo := map[string]interface{}{
			"id":         botUser.ID,
			"first_name": botUser.FirstName,
			"last_name":  botUser.LastName,
			"username":   botUser.Username,
			"phone":      botUser.Phone,
			"bot":        botUser.Bot,
			"verified":   botUser.Verified,
			"premium":    botUser.Premium,
			"token":      cfg.Token,
		}
		if err := storage.SaveJSON("bot.json", botInfo); err != nil {
			fmt.Printf("Warning: failed to save bot info: %v\n", err)
		}

		// Initialize rate limiter (30 requests per 30 seconds)
		rateLimiter := DefaultRateLimiter()
		fmt.Println("Rate limiter initialized: max 30 requests per 30 seconds")

		// Initialize components
		tgClient := tg.NewClient(client)
		dumper = NewDumper(tgClient, storage, botID, rateLimiter, cfg.ExcludeExts, cfg.OutputFileHandle)
		fmt.Printf("[DEBUG] Dumper initialized, botID=%d\n", botID)

		// Setup update handler for new messages (already registered in dispatcher)
		dispatcher.OnNewMessage(func(ctx context.Context, entities tg.Entities, u *tg.UpdateNewMessage) error {
			msg, ok := u.Message.(*tg.Message)
			if !ok || msg.Out {
				return nil
			}

			// Process new message (isNewMessage = true)
			_, err := dumper.msgProcessor.ProcessMessage(ctx, msg, &entities, true)
			if err != nil {
				fmt.Printf("Warning: failed to process new message: %v\n", err)
			}

			// Save buffered messages periodically
			if err := dumper.msgProcessor.SaveChatsTextHistory(); err != nil {
				fmt.Printf("Warning: failed to save chat history: %v\n", err)
			}

			return nil
		})

		// Dump history if not in listen-only mode
		if !cfg.ListenOnly {
			fmt.Println("Starting history dump...")
			fmt.Fprintf(os.Stderr, "[DEBUG-STDERR] listenOnly=%v\n", cfg.ListenOnly)
			fmt.Fprintf(os.Stderr, "[DEBUG-STDERR] About to call GetChatHistory\n")
			// Use GetChatHistory which works like Python version (GetMessagesRequest with ID range)
			// HistoryDumpStep = 200 (same as Python version)
			fmt.Fprintf(os.Stderr, "[DEBUG-STDERR] Calling dumper.GetChatHistory(ctx, 200, 0, %d)\n", cfg.Lookahead)
			if err := dumper.GetChatHistory(ctx, 200, 0, cfg.Lookahead); err != nil {
				fmt.Fprintf(os.Stderr, "[DEBUG-STDERR] GetChatHistory returned error: %v\n", err)
				fmt.Printf("Warning: failed to dump history: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "[DEBUG-STDERR] GetChatHistory completed successfully\n")
			}
		} else {
			fmt.Println("Listen-only mode: skipping history dump")
		}

		fmt.Println("Press Ctrl+C to stop listening for new messages...")

		// Keep running until context is canceled
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		if err == context.Canceled {
			fmt.Println("Shutdown complete")
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = destFile.ReadFrom(sourceFile)
	return err
}
