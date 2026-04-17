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
	exePath, _ := os.Executable()
	fmt.Printf("=== DUMPER VERSION: %s ===\n", VERSION)
	fmt.Printf("=== EXECUTABLE: %s ===\n", exePath)

	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer cfg.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		cancel()
	}()

	if err := runNormalMode(ctx, cfg); err != nil {
		if err == context.Canceled {
			fmt.Println("Shutdown complete")
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// ── helpers ──────────────────────────────────────────────────────────

func makeTorResolver() (dcs.Resolver, error) {
	socks5Dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:9050", nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("failed to create Tor proxy: %w", err)
	}
	contextDialer, ok := socks5Dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("SOCKS5 dialer does not support ContextDialer")
	}
	return dcs.Plain(dcs.PlainOptions{Dial: contextDialer.DialContext}), nil
}

// ── NORMAL MODE ─────────────────────────────────────────────────────

func runNormalMode(ctx context.Context, cfg *Config) error {
	botIDStr := strings.Split(cfg.Token, ":")[0]
	if botIDStr == "" {
		return fmt.Errorf("invalid bot token format")
	}

	botID, err := strconv.ParseInt(botIDStr, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse bot ID: %w", err)
	}

	basePath := botIDStr
	if _, err := os.Stat(basePath); err == nil {
		newPath := fmt.Sprintf("%s_%d", basePath, time.Now().Unix())
		if err := os.Rename(basePath, newPath); err != nil {
			fmt.Printf("Warning: failed to rename existing directory: %v\n", err)
		} else {
			fmt.Printf("Renamed existing directory to %s\n", newPath)
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
		return fmt.Errorf("failed to create base directory: %w", err)
	}

	dispatcher := tg.NewUpdateDispatcher()

	opts := telegram.Options{
		UpdateHandler: dispatcher,
	}
	if cfg.UseTor {
		resolver, err := makeTorResolver()
		if err != nil {
			return err
		}
		opts.Resolver = resolver
		fmt.Println("Tor SOCKS5 proxy enabled (127.0.0.1:9050)")
	}

	client := telegram.NewClient(cfg.ApiID, cfg.ApiHash, opts)

	return client.Run(ctx, func(ctx context.Context) error {
		if _, err := client.Auth().Bot(ctx, cfg.Token); err != nil {
			return fmt.Errorf("failed to authenticate bot: %w", err)
		}

		api := client.API()
		me, err := api.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		if err != nil {
			return fmt.Errorf("failed to get bot info: %w", err)
		}

		botUser := me.Users[0].(*tg.User)
		fmt.Printf("Authenticated as bot: %s (ID: %d)\n", botUser.FirstName, botUser.ID)

		dumper := &Dumper{}
		dumper.PrintBotInfo(botUser)

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

		rateLimiter := DefaultRateLimiter()
		fmt.Println("Rate limiter initialized: max 30 requests per 30 seconds")

		tgClient := tg.NewClient(client)
		dumper = NewDumper(tgClient, storage, botID, rateLimiter, cfg.ExcludeExts, cfg.OutputFileHandle)
		fmt.Printf("[DEBUG] Dumper initialized, botID=%d\n", botID)

		dispatcher.OnNewMessage(func(ctx context.Context, entities tg.Entities, u *tg.UpdateNewMessage) error {
			msg, ok := u.Message.(*tg.Message)
			if !ok || msg.Out {
				return nil
			}
			if _, err := dumper.msgProcessor.ProcessMessage(ctx, msg, &entities, true); err != nil {
				fmt.Printf("Warning: failed to process new message: %v\n", err)
			}
			if err := dumper.msgProcessor.SaveChatsTextHistory(); err != nil {
				fmt.Printf("Warning: failed to save chat history: %v\n", err)
			}
			return nil
		})

		if !cfg.ListenOnly {
			fmt.Println("Starting history dump...")
			if err := dumper.GetChatHistory(ctx, 200, 0, cfg.Lookahead); err != nil {
				fmt.Printf("Warning: failed to dump history: %v\n", err)
			}
		} else {
			fmt.Println("Listen-only mode: skipping history dump")
		}

		fmt.Println("Press Ctrl+C to stop listening for new messages...")
		<-ctx.Done()
		return ctx.Err()
	})
}

// ── utilities ───────────────────────────────────────────────────────

func copyFile(src, dst string) error {
	os.MkdirAll(strings.TrimSuffix(dst, "/"+dst[strings.LastIndex(dst, "/")+1:]), 0755)
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

func mustParseInt64(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		panic(fmt.Sprintf("mustParseInt64(%q): %v", s, err))
	}
	return v
}
