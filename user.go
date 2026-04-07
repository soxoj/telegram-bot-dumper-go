package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
)

// UserHandler handles user-related operations
type UserHandler struct {
	storage     *Storage
	api         *tg.Client
	allUsers    map[int64]*tg.UserFull
	rateLimiter *RateLimiter
}

// NewUserHandler creates a new UserHandler
func NewUserHandler(storage *Storage, api *tg.Client, rateLimiter *RateLimiter) *UserHandler {
	return &UserHandler{
		storage:     storage,
		api:         api,
		allUsers:    make(map[int64]*tg.UserFull),
		rateLimiter: rateLimiter,
	}
}

// PrintUserInfo prints user information to console
func (uh *UserHandler) PrintUserInfo(user *tg.User) {
	fmt.Println(strings.Repeat("=", 20))
	fmt.Printf("NEW USER DETECTED: %d\n", user.ID)
	fmt.Printf("First name: %s\n", user.FirstName)
	if user.LastName != "" {
		fmt.Printf("Last name: %s\n", user.LastName)
	}
	if user.Username != "" {
		fmt.Printf("Username: @%s - https://t.me/%s\n", user.Username, user.Username)
	} else {
		fmt.Println("User has no username")
	}
}

// SaveUserInfo saves user information to JSON file
func (uh *UserHandler) SaveUserInfo(user *tg.User) error {
	userID := strconv.FormatInt(user.ID, 10)
	
	// Create user directory
	if err := uh.storage.EnsureDir(userID); err != nil {
		return fmt.Errorf("failed to create user directory: %w", err)
	}

	// Create media directory
	if err := uh.storage.EnsureMediaDir(userID); err != nil {
		return fmt.Errorf("failed to create media directory: %w", err)
	}

	// Convert user to map for JSON serialization
	userData := map[string]interface{}{
		"id":         user.ID,
		"first_name": user.FirstName,
		"last_name":  user.LastName,
		"username":   user.Username,
		"phone":      user.Phone,
		"bot":        user.Bot,
		"verified":   user.Verified,
		"premium":    user.Premium,
	}

	// Save user JSON
	filename := fmt.Sprintf("%s/%s.json", userID, userID)
	if err := uh.storage.SaveJSON(filename, userData); err != nil {
		return fmt.Errorf("failed to save user info: %w", err)
	}

	return nil
}

// GetFullUser retrieves full user information
func (uh *UserHandler) GetFullUser(ctx context.Context, userID int64) (*tg.UserFull, error) {
	// Check cache first
	if user, ok := uh.allUsers[userID]; ok {
		return user, nil
	}

	// Get full user info from API
	inputUser := &tg.InputUser{
		UserID: userID,
	}

	// Wait for rate limiter before API call
	if err := uh.rateLimiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter error: %w", err)
	}

	result, err := uh.api.UsersGetFullUser(ctx, inputUser)
	if err != nil {
		return nil, fmt.Errorf("failed to get full user: %w", err)
	}

	// UsersGetFullUser returns *tg.UsersUserFull which contains FullUser field
	userFull := result
	fullUser := &userFull.FullUser

	// Cache the user
	uh.allUsers[userID] = fullUser

	return fullUser, nil
}

// ProcessNewUser processes a newly detected user
func (uh *UserHandler) ProcessNewUser(ctx context.Context, user *tg.User) error {
	if user == nil {
		return fmt.Errorf("user is nil")
	}

	// Check if we already processed this user
	if _, exists := uh.allUsers[user.ID]; exists {
		return nil
	}

	// Print user info
	uh.PrintUserInfo(user)

	// Save user info
	if err := uh.SaveUserInfo(user); err != nil {
		return fmt.Errorf("failed to save user info: %w", err)
	}

	// Note: RemoveOldHistory is called in ProcessMessage when new user is detected
	// to ensure old history is removed before processing messages

	// Get full user info and cache it
	fullUser, err := uh.GetFullUser(ctx, user.ID)
	if err != nil {
		// Log error but continue
		fmt.Printf("Warning: failed to get full user info: %v\n", err)
	} else {
		uh.allUsers[user.ID] = fullUser
	}

	return nil
}

// HasUser checks if user is already processed
func (uh *UserHandler) HasUser(userID int64) bool {
	_, exists := uh.allUsers[userID]
	return exists
}
