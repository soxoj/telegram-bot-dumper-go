package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gotd/td/tg"
)

// MessageProcessor handles message processing
type MessageProcessor struct {
	storage        *Storage
	mediaHandler   *MediaHandler
	userHandler    *UserHandler
	botID          int64
	messagesByChat map[string]*ChatMessages
	outputFile     *os.File
	outputMutex    sync.Mutex
}

// ChatMessages stores messages for a chat
type ChatMessages struct {
	Buf     []string
	History []string
}

// NewMessageProcessor creates a new MessageProcessor
func NewMessageProcessor(storage *Storage, mediaHandler *MediaHandler, userHandler *UserHandler, botID int64, outputFile *os.File) *MessageProcessor {
	return &MessageProcessor{
		storage:        storage,
		mediaHandler:   mediaHandler,
		userHandler:    userHandler,
		botID:          botID,
		messagesByChat: make(map[string]*ChatMessages),
		outputFile:     outputFile,
	}
}

// GetChatID extracts chat ID from message
func (mp *MessageProcessor) GetChatID(msg tg.MessageClass) string {
	switch m := msg.(type) {
	case *tg.Message:
		peer := m.PeerID
		switch p := peer.(type) {
		case *tg.PeerUser:
			// For user messages, determine chat ID based on from/to
			if m.FromID != nil {
				fromUserID := mp.getUserIDFromPeer(m.FromID)
				if fromUserID == mp.botID {
					// Message from bot, chat is with the recipient
					if m.PeerID != nil {
						if peerUser, ok := m.PeerID.(*tg.PeerUser); ok {
							return strconv.FormatInt(peerUser.UserID, 10)
						}
					}
				} else {
					// Message from user, chat is with that user
					return strconv.FormatInt(fromUserID, 10)
				}
			}
			// Fallback to peer user ID
			return strconv.FormatInt(p.UserID, 10)
		case *tg.PeerChat:
			return strconv.FormatInt(p.ChatID, 10)
		case *tg.PeerChannel:
			return strconv.FormatInt(p.ChannelID, 10)
		}
	case *tg.MessageService:
		peer := m.PeerID
		switch p := peer.(type) {
		case *tg.PeerUser:
			return strconv.FormatInt(p.UserID, 10)
		case *tg.PeerChat:
			return strconv.FormatInt(p.ChatID, 10)
		case *tg.PeerChannel:
			return strconv.FormatInt(p.ChannelID, 10)
		}
	}
	return "0"
}

// GetFromID extracts sender ID from message
func (mp *MessageProcessor) GetFromID(msg tg.MessageClass) string {
	switch m := msg.(type) {
	case *tg.Message:
		if m.FromID != nil {
			userID := mp.getUserIDFromPeer(m.FromID)
			return strconv.FormatInt(userID, 10)
		}
		// Try to get from peer
		if peer, ok := m.PeerID.(*tg.PeerUser); ok {
			return strconv.FormatInt(peer.UserID, 10)
		}
	case *tg.MessageService:
		if m.FromID != nil {
			userID := mp.getUserIDFromPeer(m.FromID)
			return strconv.FormatInt(userID, 10)
		}
	}
	return "0"
}

// ProcessMessage processes a single message
// isNewMessage indicates if this is a new message (not from history dump)
func (mp *MessageProcessor) ProcessMessage(ctx context.Context, msg tg.MessageClass, entities *tg.Entities, isNewMessage bool) (bool, error) {
	// Handle empty messages
	if _, ok := msg.(*tg.MessageEmpty); ok {
		return true, nil
	}

	m, ok := msg.(*tg.Message)
	if !ok {
		// Try MessageService
		if ms, ok := msg.(*tg.MessageService); ok {
			return mp.processServiceMessage(ctx, ms, entities, isNewMessage)
		}
		return false, nil
	}

	chatID := mp.GetChatID(msg)
	fromID := mp.GetFromID(msg)
	isFromUser := chatID == fromID

	messageText := ""

	// Process media
	if m.Media != nil {
		text, err := mp.processMedia(ctx, chatID, m.Media)
		if err != nil {
			fmt.Printf("Warning: failed to process media: %v\n", err)
		} else {
			messageText = text
		}
	}
	// Note: Action field exists only in MessageService, not in Message

	// Add message text
	if m.Message != "" {
		if messageText != "" {
			messageText += "\n" + m.Message
		} else {
			messageText = m.Message
		}
	}

	// Format message line
	dateStr := time.Unix(int64(m.Date), 0).UTC().Format("2006-01-02 15:04:05+00:00")
	text := fmt.Sprintf("[%d][%s][%s] %s", m.ID, fromID, dateStr, messageText)
	fmt.Println(text)

	// Save to output file if this is a new message and output file is specified
	if isNewMessage && mp.outputFile != nil {
		mp.outputMutex.Lock()
		_, err := mp.outputFile.WriteString(text + "\n")
		mp.outputMutex.Unlock()
		if err != nil {
			fmt.Printf("Warning: failed to write to output file: %v\n", err)
		}
	}

	// Store message
	if mp.messagesByChat[chatID] == nil {
		mp.messagesByChat[chatID] = &ChatMessages{
			Buf:     []string{},
			History: []string{},
		}
	}
	mp.messagesByChat[chatID].Buf = append(mp.messagesByChat[chatID].Buf, text)

	// Process new user if needed
	if isFromUser && fromID != "0" {
		fromIDInt, err := strconv.ParseInt(fromID, 10, 64)
		if err == nil && !mp.userHandler.HasUser(fromIDInt) {
			// Remove old history immediately when new user is detected
			// This must be done before processing messages, similar to Python version
			if err := mp.storage.RemoveOldHistory(fromID); err != nil {
				fmt.Printf("Warning: failed to remove old history: %v\n", err)
			}

			// Try to get user from entities
			var user *tg.User
			if entities != nil && entities.Users != nil {
				if u, ok := entities.Users[fromIDInt]; ok {
					user = u
				}
			}

			// If user not found in entities, fetch it
			if user == nil && m.FromID != nil {
				if peerUser, ok := m.FromID.(*tg.PeerUser); ok {
					// Fetch user info from API
					// Note: UsersGetFullUser returns UserFull which contains user info
					// We need to get the user from the full user object
					_, err := mp.userHandler.GetFullUser(ctx, peerUser.UserID)
					if err == nil {
						// User info is cached in userHandler, but we still need the basic User object
						// For now, we'll skip this as the user will be processed when found in entities
					}
				}
			}

			if user != nil {
				if err := mp.userHandler.ProcessNewUser(ctx, user); err != nil {
					fmt.Printf("Warning: failed to process new user: %v\n", err)
				}

				// Save user photos
				if err := mp.mediaHandler.SaveUserPhotos(ctx, fromIDInt); err != nil {
					fmt.Printf("Warning: failed to save user photos: %v\n", err)
				}
			}
		}
	}

	return false, nil
}

// SaveChatsTextHistory saves all buffered messages to history files
func (mp *MessageProcessor) SaveChatsTextHistory() error {
	for chatID, chatMessages := range mp.messagesByChat {
		if len(chatMessages.Buf) > 0 {
			fmt.Printf("Saving history of %s as a text...\n", chatID)
			if err := mp.storage.SaveTextHistory(chatID, chatMessages.Buf); err != nil {
				return fmt.Errorf("failed to save history for chat %s: %w", chatID, err)
			}
			chatMessages.History = append(chatMessages.History, chatMessages.Buf...)
			chatMessages.Buf = []string{}
		}
	}
	return nil
}

// getUserIDFromPeer extracts user ID from peer
func (mp *MessageProcessor) getUserIDFromPeer(peer tg.PeerClass) int64 {
	switch p := peer.(type) {
	case *tg.PeerUser:
		return p.UserID
	}
	return 0
}

// processMedia processes media in a message
func (mp *MessageProcessor) processMedia(ctx context.Context, chatID string, media tg.MessageMediaClass) (string, error) {
	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		photo := m.Photo
		if photo != nil {
			if err := mp.mediaHandler.SaveMediaPhoto(ctx, chatID, photo); err != nil {
				return "", err
			}
			// Get photo ID
			if p, ok := photo.(*tg.Photo); ok {
				return fmt.Sprintf("Photo: media/%d.jpg", p.ID), nil
			}
			return "Photo: media/photo.jpg", nil
		}
	case *tg.MessageMediaDocument:
		doc := m.Document
		if doc, ok := doc.(*tg.Document); ok {
			if err := mp.mediaHandler.SaveMediaDocument(ctx, chatID, doc); err != nil {
				return "", err
			}
			filename := mp.mediaHandler.GetMediaFilename(chatID, doc.ID, "bin")
			baseName := filepath.Base(filename)
			return fmt.Sprintf("Document: media/%s", baseName), nil
		}
	case *tg.MessageMediaGeo:
		geo := m.Geo
		if geo, ok := geo.(*tg.GeoPoint); ok {
			return fmt.Sprintf("Geoposition: %f, %f", geo.Long, geo.Lat), nil
		}
	case *tg.MessageMediaContact:
		contact := m
		return fmt.Sprintf("Vcard: phone %s, %s %s, rawdata %s",
			contact.PhoneNumber, contact.FirstName, contact.LastName, contact.Vcard), nil
	default:
		return fmt.Sprintf("Media: %T", media), nil
	}
	return "", nil
}

// processAction processes action in a service message
func (mp *MessageProcessor) processAction(ctx context.Context, chatID string, action tg.MessageActionClass) (string, error) {
	switch a := action.(type) {
	case *tg.MessageActionChatEditPhoto:
		photo := a.Photo
		if photo != nil {
			if err := mp.mediaHandler.SaveMediaPhoto(ctx, chatID, photo); err != nil {
				return "", err
			}
			if p, ok := photo.(*tg.Photo); ok {
				return fmt.Sprintf("Photo of chat was changed: media/%d.jpg", p.ID), nil
			}
			return "Photo of chat was changed: media/photo.jpg", nil
		}
	default:
		return fmt.Sprintf("Action: %T", action), nil
	}
	return "", nil
}

// processServiceMessage processes a service message
func (mp *MessageProcessor) processServiceMessage(ctx context.Context, msg *tg.MessageService, entities *tg.Entities, isNewMessage bool) (bool, error) {
	chatID := mp.GetChatID(msg)
	fromID := mp.GetFromID(msg)

	messageText := ""
	if msg.Action != nil {
		text, err := mp.processAction(ctx, chatID, msg.Action)
		if err != nil {
			fmt.Printf("Warning: failed to process service action: %v\n", err)
		} else {
			messageText = text
		}
	}

	dateStr := time.Unix(int64(msg.Date), 0).UTC().Format("2006-01-02 15:04:05+00:00")
	text := fmt.Sprintf("[%d][%s][%s] %s", msg.ID, fromID, dateStr, messageText)
	fmt.Println(text)

	// Save to output file if this is a new message and output file is specified
	// Note: service messages from history are not saved to output file
	if isNewMessage && mp.outputFile != nil {
		mp.outputMutex.Lock()
		_, err := mp.outputFile.WriteString(text + "\n")
		mp.outputMutex.Unlock()
		if err != nil {
			fmt.Printf("Warning: failed to write to output file: %v\n", err)
		}
	}

	if mp.messagesByChat[chatID] == nil {
		mp.messagesByChat[chatID] = &ChatMessages{
			Buf:     []string{},
			History: []string{},
		}
	}
	mp.messagesByChat[chatID].Buf = append(mp.messagesByChat[chatID].Buf, text)

	return false, nil
}
