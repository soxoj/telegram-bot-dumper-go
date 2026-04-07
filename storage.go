package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Storage handles file operations for the dumper
type Storage struct {
	basePath string
}

// NewStorage creates a new Storage instance
func NewStorage(basePath string) *Storage {
	return &Storage{
		basePath: basePath,
	}
}

// EnsureDir ensures that a directory exists, creating it if necessary
func (s *Storage) EnsureDir(path string) error {
	fullPath := filepath.Join(s.basePath, path)
	return os.MkdirAll(fullPath, 0755)
}

// SaveJSON saves data as JSON to a file
func (s *Storage) SaveJSON(filename string, data interface{}) error {
	fullPath := filepath.Join(s.basePath, filename)
	file, err := os.Create(fullPath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", fullPath, err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}

	return nil
}

// SaveTextHistory appends text to a history file
func (s *Storage) SaveTextHistory(chatID string, messages []string) error {
	chatDir := filepath.Join(s.basePath, chatID)
	if err := s.EnsureDir(chatDir); err != nil {
		return fmt.Errorf("failed to create chat directory: %w", err)
	}

	historyFile := filepath.Join(chatDir, fmt.Sprintf("%s_history.txt", chatID))
	file, err := os.OpenFile(historyFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open history file: %w", err)
	}
	defer file.Close()

	content := strings.Join(messages, "\n") + "\n"
	if _, err := file.WriteString(content); err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	return nil
}

// RemoveOldHistory removes old history file for a chat
func (s *Storage) RemoveOldHistory(chatID string) error {
	historyFile := filepath.Join(s.basePath, chatID, fmt.Sprintf("%s_history.txt", chatID))
	if _, err := os.Stat(historyFile); err == nil {
		if err := os.Remove(historyFile); err != nil {
			return fmt.Errorf("failed to remove old history: %w", err)
		}
		fmt.Printf("Removing old history of %s...\n", chatID)
	}
	return nil
}

// SaveFile saves a file to the specified path
func (s *Storage) SaveFile(relativePath string, data []byte) error {
	fullPath := filepath.Join(s.basePath, relativePath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	return os.WriteFile(fullPath, data, 0644)
}

// FileExists checks if a file exists
func (s *Storage) FileExists(relativePath string) bool {
	fullPath := filepath.Join(s.basePath, relativePath)
	_, err := os.Stat(fullPath)
	return err == nil
}

// GetMediaDir returns the media directory path for a chat
func (s *Storage) GetMediaDir(chatID string) string {
	return filepath.Join(s.basePath, chatID, "media")
}

// EnsureMediaDir ensures the media directory exists for a chat
func (s *Storage) EnsureMediaDir(chatID string) error {
	mediaDir := s.GetMediaDir(chatID)
	return os.MkdirAll(mediaDir, 0755)
}
