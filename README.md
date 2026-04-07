# telegram-bot-dumper (Go version)

Easy dumping of all Telegram bot stuff.

**Input**: bot token, API ID, and API Hash.

**Output**: bot name & info, all chats text history & media, bot's users info & photos.

## Requirements

- Go >= 1.21
- [Register Telegram application](https://core.telegram.org/api/obtaining_api_id) to get API_ID and API_HASH

## Installation

```sh
cd tg_go
go mod download
go build -o dumper
```

## Usage

```sh
./dumper --api-id YOUR_API_ID --api-hash "YOUR_API_HASH" --token "BOT_TOKEN"
```

### Command Line Arguments

- `--api-id` (required): Telegram API ID
- `--api-hash` (required): Telegram API Hash  
- `--token`: Bot token (can also be entered interactively if not provided)
- `--listen-only`: Only listen for new messages, don't dump history
- `--lookahead`: Number of additional cycles to skip empty messages (default: 0)
- `--tor`: Enable Tor SOCKS5 proxy (127.0.0.1:9050)
- `--exclude-exts`: Comma-separated list of file extensions to exclude from downloading (e.g., `pdf,php,txt`)
- `--exclude-exts-file`: Path to file with excluded extensions (one per line). Cannot be used together with `--exclude-exts`
- `--output-file`: Path to file where output for NEW messages will be saved (appended)

### Examples

Basic usage:
```sh
./dumper --api-id 12345 --api-hash "abcdef123456" --token "12345678:ABCDEFJHKLMNOPQRSTUVWXYZ"
```

With lookahead (check additional 5*200 = 1000 messages):
```sh
./dumper --api-id 12345 --api-hash "abcdef123456" --token "BOT_TOKEN" --lookahead 5
```

With Tor proxy:
```sh
./dumper --api-id 12345 --api-hash "abcdef123456" --token "BOT_TOKEN" --tor
```

Listen-only mode (no history dump):
```sh
./dumper --api-id 12345 --api-hash "abcdef123456" --token "BOT_TOKEN" --listen-only
```

Exclude specific file extensions (comma-separated):
```sh
./dumper --api-id 12345 --api-hash "abcdef123456" --token "BOT_TOKEN" --exclude-exts "pdf,php,txt"
```

Exclude file extensions from file:
```sh
./dumper --api-id 12345 --api-hash "abcdef123456" --token "BOT_TOKEN" --exclude-exts-file "excluded.txt"
```

Save new messages output to file:
```sh
./dumper --api-id 12345 --api-hash "abcdef123456" --token "BOT_TOKEN" --output-file "new_messages.log"
```

## Output Structure

The dumper creates a directory named after the bot ID (e.g., `1234567890/`) containing:

- `bot.json` - Bot information
- `{user_id}/` - Directory for each user
  - `{user_id}.json` - User information
  - `{user_id}_history.txt` - Text history with the user
  - `media/` - Media files (photos, documents)
    - `{photo_id}.jpg` - Photos
    - `{document_id}.{ext}` - Documents
  - `{photo_id}.jpg` - User profile photos

## Differences from Python Version

- API_ID and API_HASH are required command-line arguments (not hardcoded)
- Uses gotd/td library instead of Telethon
- Written in Go for better performance and easier deployment

## Known Issues

1. **Bot is exiting with not fully dumped history**

Some messages can be deleted by bot users. If you suppose that the history was not completely dumped, specify cycles count to skip empty messages (200 per cycle by default):

```sh
./dumper --api-id 12345 --api-hash "abcdef" --token "BOT_TOKEN" --lookahead 5
```

2. **History was not dumped for chats**

This is a known limitation. The dumper attempts to get all chats, but some edge cases may not be handled.

## Testing

You can test the dumper with a test bot token:

```sh
TEST_TOKEN="your_test_token" go test ./...
```

## License

See LICENSE file in the parent directory.
