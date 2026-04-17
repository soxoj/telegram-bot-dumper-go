# telegram-bot-dumper (Go version)

Easy dumping of all Telegram bot stuff — with an advanced **trap mode** that creates groups, invites bots, discovers their accessible chats, and forwards all messages to your trap groups.

## Installation

```sh
go mod download
go build -o dumper
```

## First Run

On first run you'll be prompted to enter your Telegram API credentials:

```
╔══════════════════════════════════════════════════════════╗
║              FIRST-RUN SETUP                            ║
║                                                         ║
║  Register a Telegram app at https://my.telegram.org     ║
║  to get your API ID and API Hash.                       ║
║                                                         ║
║  These will be saved to config.json so you only         ║
║  have to do this once.                                  ║
╚══════════════════════════════════════════════════════════╝

  API ID: 12345678
  API Hash: abcdef1234567890abcdef1234567890
  Phone number (with country code, e.g. 14155551234): 14155551234

  ✓ Saved to config.json
```

Credentials are stored in `config.json` next to the binary. You'll never be asked again unless you delete it.

The phone number is only needed for **trap mode** (user account auth). For normal bot dumping you can leave it blank.

## Normal Mode

Standard bot dumper — authenticates as a bot and extracts message history.

```sh
# With token flag
./dumper --token "12345678:ABCDEF..."

# Interactive (prompts for token)
./dumper
```

### Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--token` | Bot token (interactive prompt if omitted) | — |
| `--listen-only` | Only listen for new messages, skip history dump | false |
| `--lookahead` | Additional empty-message cycles to check | 0 |
| `--tor` | Enable Tor SOCKS5 proxy (127.0.0.1:9050) | false |
| `--exclude-exts` | Comma-separated file extensions to skip | — |
| `--exclude-exts-file` | File with excluded extensions (one per line) | — |
| `--output-file` | File to append new messages to | — |

## 🪤 Trap Mode

Trap mode uses your **user account** to create groups, invite target bots, then uses the bot's own MTProto session to discover all its accessible chats and **forward every message** to your trap groups.

### How It Works

1. User account authenticates (OTP + 2FA on first run, session cached after)
2. For each bot token:
   - Bot authenticates via MTProto → gets its username
   - User creates a trap group and adds the bot
   - Bot's message ID space is scanned to discover all accessible chats
   - All messages forwarded to the trap group
3. Results saved per-bot with chat manifests

### Usage

```sh
# Single bot — one dedicated trap group
./dumper --trap --token "BOT_TOKEN"

# Multiple bots from file — one group per bot
./dumper --trap --tokens-file tokens.txt

# Multiple bots — shared group
./dumper --trap --trap-mode shared --tokens-file tokens.txt

# Deep scan with custom prefix
./dumper --trap --trap-prefix "harvest" --max-msg-id 50000 --lookahead 10 --tokens-file tokens.txt

# Trap + normal dump simultaneously
./dumper --trap --dump-and-forward --token "BOT_TOKEN"

# Reuse existing group for shared mode
./dumper --trap --trap-mode shared --trap-group-id 123456789 --tokens-file tokens.txt
```

### Trap Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--trap` | **Enable trap mode** | false |
| `--trap-mode` | `per-bot` or `shared` | per-bot |
| `--trap-group-id` | Existing group ID for shared mode (0 = create) | 0 |
| `--trap-prefix` | Prefix for auto-created group names | dump |
| `--tokens-file` | File with bot tokens (one per line) | — |
| `--max-msg-id` | Discovery scan depth | 10000 |
| `--dump-and-forward` | Also run normal dump alongside forwarding | false |

### Tokens File Format

```
12345678:AABBccDDeeFFggHH
87654321:ZZYYxxWWvvUUttSS
# Comments and blank lines are ignored
```

### Output

```
trap_12345678/
├── trap_12345678.json         # Results summary
└── trap_12345678_chats.json   # Discovered chats manifest
```

## Output Structure (Normal Mode)

```
1234567890/
├── bot.json
├── {user_id}/
│   ├── {user_id}.json
│   ├── {user_id}_history.txt
│   └── media/
│       ├── {photo_id}.jpg
│       └── {document_id}.{ext}
└── {photo_id}.jpg
```

## Files

| File | Purpose |
|------|---------|
| `config.json` | API credentials (auto-created, gitignored) |
| `user_session.json` | Telegram user session (auto-created, gitignored) |

## Known Issues

1. **Incomplete history** — Some messages may be deleted. Use `--lookahead` to check additional cycles.
2. **First-run auth** — Trap mode prompts for OTP code (and 2FA if enabled) on first run. After that, `user_session.json` is reused.
