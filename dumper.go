package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gotd/td/tg"
)

const (
	// HistoryDumpStep is the number of messages to fetch per cycle
	HistoryDumpStep = 200
)

// Dumper handles the main dumping logic
type Dumper struct {
	api             *tg.Client
	storage         *Storage
	mediaHandler    *MediaHandler
	userHandler     *UserHandler
	msgProcessor    *MessageProcessor
	botID           int64
	emptyMsgCounter int
	rateLimiter     *RateLimiter
}

// NewDumper creates a new Dumper instance
func NewDumper(api *tg.Client, storage *Storage, botID int64, rateLimiter *RateLimiter, excludedExts map[string]bool, outputFile *os.File) *Dumper {
	mediaHandler := NewMediaHandler(storage, api, rateLimiter, excludedExts)
	userHandler := NewUserHandler(storage, api, rateLimiter)
	msgProcessor := NewMessageProcessor(storage, mediaHandler, userHandler, botID, outputFile)

	return &Dumper{
		api:             api,
		storage:         storage,
		mediaHandler:    mediaHandler,
		userHandler:     userHandler,
		msgProcessor:    msgProcessor,
		botID:           botID,
		emptyMsgCounter: 0,
		rateLimiter:     rateLimiter,
	}
}

// GetChatHistory dumps chat history in batches
// This method uses MessagesGetMessages (like Python version) and does NOT use MessagesGetDialogs
// Note: MessagesGetDialogs is only available for user accounts, not bots
func (d *Dumper) GetChatHistory(ctx context.Context, fromID, toID int, lookahead int) error {
	fmt.Fprintf(os.Stderr, "[DEBUG-STDERR] GetChatHistory called: fromID=%d, toID=%d\n", fromID, toID)
	fmt.Printf("Dumping history from %d to %d (using MessagesGetMessages, not GetDialogs)...\n", fromID, toID)

	// Create message ID range
	// In Telegram, higher IDs are newer messages
	// We need to request messages in reverse order
	ids := make([]tg.InputMessageClass, 0, fromID-toID)
	for i := toID; i < fromID; i++ {
		ids = append(ids, &tg.InputMessageID{
			ID: i,
		})
	}

	// Wait for rate limiter before API call
	if err := d.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error: %w", err)
	}

	// Get messages using MessagesGetMessages (NOT MessagesGetDialogs - that's only for user accounts)
	result, err := d.api.MessagesGetMessages(ctx, ids)
	if err != nil {
		return fmt.Errorf("failed to get messages (using MessagesGetMessages): %w", err)
	}

	messages, ok := result.(*tg.MessagesMessages)
	if !ok {
		return fmt.Errorf("unexpected messages type")
	}

	// Create entities from messages result
	entities := &tg.Entities{
		Users: convertUsersToMap(messages.Users),
		Chats: convertChatsToMap(messages.Chats),
	}

	// Process messages
	historyTail := true
	for _, msg := range messages.Messages {
		isEmpty, err := d.msgProcessor.ProcessMessage(ctx, msg, entities, false)
		if err != nil {
			fmt.Printf("Warning: failed to process message: %v\n", err)
			continue
		}

		if isEmpty {
			d.emptyMsgCounter++
		} else {
			if d.emptyMsgCounter > 0 {
				fmt.Printf("Empty messages x%d\n", d.emptyMsgCounter)
				d.emptyMsgCounter = 0
			}
			historyTail = false
		}
	}

	// Save buffered messages
	if err := d.msgProcessor.SaveChatsTextHistory(); err != nil {
		return fmt.Errorf("failed to save chat history: %w", err)
	}

	// Continue dumping if not at tail
	if !historyTail {
		return d.GetChatHistory(ctx, fromID+HistoryDumpStep, toID+HistoryDumpStep, lookahead)
	}

	// Handle lookahead
	if lookahead > 0 {
		fmt.Printf("Lookahead: checking %d more cycles...\n", lookahead)
		return d.GetChatHistory(ctx, fromID+HistoryDumpStep, toID+HistoryDumpStep, lookahead-1)
	}

	fmt.Println("History was fully dumped.")
	return nil
}

// GetChatHistoryByOffset uses offset-based pagination (more reliable)
func (d *Dumper) GetChatHistoryByOffset(ctx context.Context, lookahead int) error {
	offsetID := 0
	limit := HistoryDumpStep
	emptyCount := 0
	var offsetPeer tg.InputPeerClass = &tg.InputPeerSelf{}

	for {
		fmt.Printf("Dumping history with offset %d...\n", offsetID)

		// Wait for rate limiter before API call
		if err := d.rateLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter error: %w", err)
		}

		// Get dialogs to find all chats
		dialogs, err := d.api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetPeer:  offsetPeer,
			OffsetID:    offsetID,
			OffsetDate:  0,
			Limit:       limit,
			Hash:        0,
		})
		if err != nil {
			return fmt.Errorf("failed to get dialogs: %w", err)
		}

		dialogsResult, ok := dialogs.(*tg.MessagesDialogs)
		if !ok {
			// Try MessagesDialogsSlice
			if dialogsSlice, ok := dialogs.(*tg.MessagesDialogsSlice); ok {
				dialogsResult = &tg.MessagesDialogs{
					Dialogs: dialogsSlice.Dialogs,
					Messages: dialogsSlice.Messages,
					Chats: dialogsSlice.Chats,
					Users: dialogsSlice.Users,
				}
			} else {
				return fmt.Errorf("unexpected dialogs type")
			}
		}

		// Process messages from dialogs
		// Create entities from dialogs result
		entities := &tg.Entities{
			Users: convertUsersToMap(dialogsResult.Users),
			Chats: convertChatsToMap(dialogsResult.Chats),
		}

		foundMessages := false
		for _, msg := range dialogsResult.Messages {
			isEmpty, err := d.msgProcessor.ProcessMessage(ctx, msg, entities, false)
			if err != nil {
				fmt.Printf("Warning: failed to process message: %v\n", err)
				continue
			}

			if isEmpty {
				emptyCount++
			} else {
				if emptyCount > 0 {
					fmt.Printf("Empty messages x%d\n", emptyCount)
					emptyCount = 0
				}
				foundMessages = true
			}
		}

		// Save buffered messages
		if err := d.msgProcessor.SaveChatsTextHistory(); err != nil {
			return fmt.Errorf("failed to save chat history: %w", err)
		}

		// Check if we should continue
		if !foundMessages {
			if lookahead > 0 {
				lookahead--
				fmt.Printf("Lookahead: checking %d more cycles...\n", lookahead)
				continue
			}
			fmt.Println("History was fully dumped.")
			break
		}

		// Update offset for next iteration
		if len(dialogsResult.Messages) > 0 {
			lastMsg := dialogsResult.Messages[len(dialogsResult.Messages)-1]
			if msg, ok := lastMsg.(*tg.Message); ok {
				offsetID = msg.ID
				// Update offsetPeer for pagination
				switch p := msg.PeerID.(type) {
				case *tg.PeerUser:
					offsetPeer = &tg.InputPeerUser{UserID: p.UserID}
				case *tg.PeerChat:
					offsetPeer = &tg.InputPeerChat{ChatID: p.ChatID}
				case *tg.PeerChannel:
					// Need access hash, get from dialogs result
					for _, chatClass := range dialogsResult.Chats {
						if channel, ok := chatClass.(*tg.Channel); ok && channel.ID == p.ChannelID {
							offsetPeer = &tg.InputPeerChannel{
								ChannelID: p.ChannelID,
								AccessHash: channel.AccessHash,
							}
							break
						}
					}
				}
			} else if msgSvc, ok := lastMsg.(*tg.MessageService); ok {
				offsetID = msgSvc.ID
				// Update offsetPeer for pagination
				switch p := msgSvc.PeerID.(type) {
				case *tg.PeerUser:
					offsetPeer = &tg.InputPeerUser{UserID: p.UserID}
				case *tg.PeerChat:
					offsetPeer = &tg.InputPeerChat{ChatID: p.ChatID}
				case *tg.PeerChannel:
					// Need access hash, get from dialogs result
					for _, chatClass := range dialogsResult.Chats {
						if channel, ok := chatClass.(*tg.Channel); ok && channel.ID == p.ChannelID {
							offsetPeer = &tg.InputPeerChannel{
								ChannelID: p.ChannelID,
								AccessHash: channel.AccessHash,
							}
							break
						}
					}
				}
			}
		}

		// If we got fewer messages than requested, we're done
		if len(dialogsResult.Messages) < limit {
			if lookahead > 0 {
				lookahead--
				fmt.Printf("Lookahead: checking %d more cycles...\n", lookahead)
				continue
			}
			fmt.Println("History was fully dumped.")
			break
		}
	}

	return nil
}

// GetChatHistorySimple uses a simpler approach with GetHistory
func (d *Dumper) GetChatHistorySimple(ctx context.Context, lookahead int) error {
	// Wait for rate limiter before API call
	if err := d.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error: %w", err)
	}

	// First, get all dialogs to find chats
	// Use InputPeerSelf for the first request (represents the bot itself)
	dialogs, err := d.api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerSelf{},
		OffsetID:   0,
		OffsetDate: 0,
		Limit:      100,
		Hash:       0,
	})
	if err != nil {
		return fmt.Errorf("failed to get dialogs: %w", err)
	}

	var allChats []tg.InputPeerClass

	switch result := dialogs.(type) {
	case *tg.MessagesDialogs:
		for _, chat := range result.Chats {
			switch c := chat.(type) {
			case *tg.Chat:
				allChats = append(allChats, &tg.InputPeerChat{ChatID: c.ID})
			case *tg.Channel:
				allChats = append(allChats, &tg.InputPeerChannel{
					ChannelID: c.ID,
					AccessHash: c.AccessHash,
				})
			}
		}
		// Also add user peers
		for _, userClass := range result.Users {
			if user, ok := userClass.(*tg.User); ok {
				if user.Bot {
					continue // Skip bots
				}
				allChats = append(allChats, &tg.InputPeerUser{
					UserID: user.ID,
				})
			}
		}
	case *tg.MessagesDialogsSlice:
		for _, chat := range result.Chats {
			switch c := chat.(type) {
			case *tg.Chat:
				allChats = append(allChats, &tg.InputPeerChat{ChatID: c.ID})
			case *tg.Channel:
				allChats = append(allChats, &tg.InputPeerChannel{
					ChannelID: c.ID,
					AccessHash: c.AccessHash,
				})
			}
		}
		for _, userClass := range result.Users {
			if user, ok := userClass.(*tg.User); ok {
				if user.Bot {
					continue
				}
				allChats = append(allChats, &tg.InputPeerUser{
					UserID: user.ID,
				})
			}
		}
	}

	// Process each chat
	for _, peer := range allChats {
		if err := d.dumpChatHistory(ctx, peer, lookahead); err != nil {
			fmt.Printf("Warning: failed to dump history for peer: %v\n", err)
			continue
		}
	}

	// Also process messages from all dialogs
	fmt.Println("Processing messages from dialogs...")
	switch result := dialogs.(type) {
	case *tg.MessagesDialogs:
		entities := &tg.Entities{
			Users: convertUsersToMap(result.Users),
			Chats: convertChatsToMap(result.Chats),
		}
		for _, msg := range result.Messages {
			if _, err := d.msgProcessor.ProcessMessage(ctx, msg, entities, false); err != nil {
				fmt.Printf("Warning: failed to process message: %v\n", err)
			}
		}
	case *tg.MessagesDialogsSlice:
		entities := &tg.Entities{
			Users: convertUsersToMap(result.Users),
			Chats: convertChatsToMap(result.Chats),
		}
		for _, msg := range result.Messages {
			if _, err := d.msgProcessor.ProcessMessage(ctx, msg, entities, false); err != nil {
				fmt.Printf("Warning: failed to process message: %v\n", err)
			}
		}
	}

	if err := d.msgProcessor.SaveChatsTextHistory(); err != nil {
		return fmt.Errorf("failed to save chat history: %w", err)
	}

	return nil
}

// dumpChatHistory dumps history for a specific chat
func (d *Dumper) dumpChatHistory(ctx context.Context, peer tg.InputPeerClass, lookahead int) error {
	offsetID := 0
	limit := HistoryDumpStep
	emptyCount := 0
	maxEmpty := lookahead * 10 // Allow some empty messages

	for {
		// Wait for rate limiter before API call
		if err := d.rateLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter error: %w", err)
		}

		history, err := d.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:      peer,
			OffsetID:  offsetID,
			OffsetDate: 0,
			AddOffset: 0,
			Limit:     limit,
			MaxID:     0,
			MinID:     0,
			Hash:      0,
		})
		if err != nil {
			return fmt.Errorf("failed to get history: %w", err)
		}

		messages, ok := history.(*tg.MessagesMessages)
		if !ok {
			return fmt.Errorf("unexpected history type")
		}

		if len(messages.Messages) == 0 {
			if emptyCount < maxEmpty {
				emptyCount++
				continue
			}
			break
		}

		// Create entities from history result
		entities := &tg.Entities{
			Users: convertUsersToMap(messages.Users),
			Chats: convertChatsToMap(messages.Chats),
		}

		foundMessages := false
		for _, msg := range messages.Messages {
			isEmpty, err := d.msgProcessor.ProcessMessage(ctx, msg, entities, false)
			if err != nil {
				fmt.Printf("Warning: failed to process message: %v\n", err)
				continue
			}

			if isEmpty {
				emptyCount++
			} else {
				if emptyCount > 0 {
					fmt.Printf("Empty messages x%d\n", emptyCount)
					emptyCount = 0
				}
				foundMessages = true
			}
		}

		// Update offset
		if len(messages.Messages) > 0 {
			lastMsg := messages.Messages[len(messages.Messages)-1]
			if msg, ok := lastMsg.(*tg.Message); ok {
				offsetID = msg.ID
			} else if msgSvc, ok := lastMsg.(*tg.MessageService); ok {
				offsetID = msgSvc.ID
			}
		}

		if !foundMessages && emptyCount >= maxEmpty {
			break
		}

		if len(messages.Messages) < limit {
			break
		}
	}

	return nil
}

// PrintBotInfo prints bot information
func (d *Dumper) PrintBotInfo(bot *tg.User) {
	fmt.Printf("ID: %d\n", bot.ID)
	fmt.Printf("Name: %s\n", bot.FirstName)
	if bot.Username != "" {
		fmt.Printf("Username: @%s - https://t.me/%s\n", bot.Username, bot.Username)
	}
}

// convertUsersToMap converts []tg.UserClass to map[int64]*tg.User
func convertUsersToMap(users []tg.UserClass) map[int64]*tg.User {
	result := make(map[int64]*tg.User)
	for _, u := range users {
		if user, ok := u.(*tg.User); ok {
			result[user.ID] = user
		}
	}
	return result
}

// convertChatsToMap converts []tg.ChatClass to map[int64]*tg.Chat
func convertChatsToMap(chats []tg.ChatClass) map[int64]*tg.Chat {
	result := make(map[int64]*tg.Chat)
	for _, c := range chats {
		if chat, ok := c.(*tg.Chat); ok {
			result[chat.ID] = chat
		}
	}
	return result
}

// convertUsersToSlice converts []tg.UserClass to []*tg.User
func convertUsersToSlice(users []tg.UserClass) []*tg.User {
	result := make([]*tg.User, 0, len(users))
	for _, u := range users {
		if user, ok := u.(*tg.User); ok {
			result = append(result, user)
		}
	}
	return result
}
