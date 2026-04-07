package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
)

// MediaHandler handles media download operations
type MediaHandler struct {
	storage      *Storage
	api          *tg.Client
	rateLimiter  *RateLimiter
	excludedExts map[string]bool
}

// NewMediaHandler creates a new MediaHandler
func NewMediaHandler(storage *Storage, api *tg.Client, rateLimiter *RateLimiter, excludedExts map[string]bool) *MediaHandler {
	return &MediaHandler{
		storage:      storage,
		api:          api,
		rateLimiter:  rateLimiter,
		excludedExts: excludedExts,
	}
}

// SaveMediaPhoto downloads and saves a photo
func (mh *MediaHandler) SaveMediaPhoto(ctx context.Context, chatID string, photo tg.PhotoClass) error {
	photoObj, ok := photo.(*tg.Photo)
	if !ok {
		return fmt.Errorf("unexpected photo type")
	}

	// Get the largest photo size
	var largestSize *tg.PhotoSize
	for _, size := range photoObj.Sizes {
		if ps, ok := size.(*tg.PhotoSize); ok {
			if largestSize == nil || ps.W > largestSize.W {
				largestSize = ps
			}
		}
	}

	if largestSize == nil {
		return fmt.Errorf("no photo size found")
	}

	// Ensure media directory exists
	if err := mh.storage.EnsureMediaDir(chatID); err != nil {
		return fmt.Errorf("failed to create media directory: %w", err)
	}

	// Download photo
	inputFile := &tg.InputPhotoFileLocation{
		ID:            photoObj.ID,
		AccessHash:    photoObj.AccessHash,
		FileReference: photoObj.FileReference,
		ThumbSize:     largestSize.Type,
	}

	filename := fmt.Sprintf("%d.jpg", photoObj.ID)
	filePath := filepath.Join(mh.storage.GetMediaDir(chatID), filename)

	if err := mh.downloadFile(ctx, inputFile, filePath); err != nil {
		return fmt.Errorf("failed to download photo: %w", err)
	}

	fmt.Printf("Saving photo %d...\n", photoObj.ID)
	return nil
}

// SaveMediaDocument downloads and saves a document
func (mh *MediaHandler) SaveMediaDocument(ctx context.Context, chatID string, document *tg.Document) error {
	// Get filename from document attributes
	filename := mh.getDocumentFilename(document)
	if filename == "" {
		// Fallback to document ID with mime type
		ext := "bin"
		if document.MimeType != "" {
			parts := strings.Split(document.MimeType, "/")
			if len(parts) > 1 && parts[1] != "" {
				ext = parts[1]
			}
		}
		filename = fmt.Sprintf("%d.%s", document.ID, ext)
	}

	// Check if file extension should be excluded
	if len(mh.excludedExts) > 0 {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
		if mh.excludedExts[ext] {
			fmt.Printf("Skipping document %s (extension %s is excluded)\n", filename, ext)
			return nil
		}
	}

	// Ensure media directory exists
	if err := mh.storage.EnsureMediaDir(chatID); err != nil {
		return fmt.Errorf("failed to create media directory: %w", err)
	}

	filePath := filepath.Join(mh.storage.GetMediaDir(chatID), filename)

	// Check if file exists, add suffix if needed
	if mh.storage.FileExists(filepath.Join(chatID, "media", filename)) {
		ext := filepath.Ext(filename)
		base := strings.TrimSuffix(filename, ext)
		filename = fmt.Sprintf("%s_%d%s", base, document.ID, ext)
		filePath = filepath.Join(mh.storage.GetMediaDir(chatID), filename)
	}

	// Download document
	inputFile := &tg.InputDocumentFileLocation{
		ID:            document.ID,
		AccessHash:    document.AccessHash,
		FileReference: document.FileReference,
	}

	if err := mh.downloadFile(ctx, inputFile, filePath); err != nil {
		return fmt.Errorf("failed to download document: %w", err)
	}

	return nil
}

// SaveUserPhotos downloads and saves user profile photos
func (mh *MediaHandler) SaveUserPhotos(ctx context.Context, userID int64) error {
	userIDStr := strconv.FormatInt(userID, 10)

	// Get user photos
	inputUser := &tg.InputUser{
		UserID: userID,
	}

	// Wait for rate limiter before API call
	if err := mh.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter error: %w", err)
	}

	result, err := mh.api.PhotosGetUserPhotos(ctx, &tg.PhotosGetUserPhotosRequest{
		UserID: inputUser,
		Offset: 0,
		MaxID:  0,
		Limit:  100,
	})
	if err != nil {
		return fmt.Errorf("failed to get user photos: %w", err)
	}

	photos, ok := result.(*tg.PhotosPhotos)
	if !ok {
		return fmt.Errorf("unexpected photos type")
	}

	// Download each photo
	for _, photo := range photos.Photos {
		if err := mh.SaveMediaPhoto(ctx, userIDStr, photo); err != nil {
			fmt.Printf("Warning: failed to save user photo: %v\n", err)
			continue
		}
	}

	return nil
}

// GetMediaFilename returns the relative filename for media
func (mh *MediaHandler) GetMediaFilename(chatID string, mediaID int64, ext string) string {
	return filepath.Join(chatID, "media", fmt.Sprintf("%d.%s", mediaID, ext))
}

// getDocumentFilename extracts filename from document attributes
func (mh *MediaHandler) getDocumentFilename(document *tg.Document) string {
	for _, attr := range document.Attributes {
		switch a := attr.(type) {
		case *tg.DocumentAttributeFilename:
			return a.FileName
		case *tg.DocumentAttributeAudio:
			ext := "mp3"
			if document.MimeType != "" {
				parts := strings.Split(document.MimeType, "/")
				if len(parts) > 1 && parts[1] != "" {
					ext = parts[1]
				}
			}
			return fmt.Sprintf("%d.%s", document.ID, ext)
		case *tg.DocumentAttributeVideo:
			ext := "mp4"
			if document.MimeType != "" {
				parts := strings.Split(document.MimeType, "/")
				if len(parts) > 1 && parts[1] != "" {
					ext = parts[1]
				}
			}
			return fmt.Sprintf("%d.%s", document.ID, ext)
		}
	}
	return ""
}

// downloadFile downloads a file using UploadGetFile
func (mh *MediaHandler) downloadFile(ctx context.Context, location tg.InputFileLocationClass, filePath string) error {
	// Create file
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	offset := int64(0)
	chunkSize := int(1024 * 1024) // 1MB chunks

	for {
		// Wait for rate limiter before API call
		if err := mh.rateLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter error: %w", err)
		}

		result, err := mh.api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   offset,
			Limit:    chunkSize,
		})
		if err != nil {
			return fmt.Errorf("failed to get file chunk: %w", err)
		}

		fileResult, ok := result.(*tg.UploadFile)
		if !ok {
			return fmt.Errorf("unexpected file result type")
		}

		// Write chunk to file
		if _, err := file.Write(fileResult.Bytes); err != nil {
			return fmt.Errorf("failed to write chunk: %w", err)
		}

		// Check if we've downloaded everything
		if len(fileResult.Bytes) < chunkSize {
			break
		}

		offset += int64(len(fileResult.Bytes))
	}

	return nil
}
