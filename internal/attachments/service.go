package attachments

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/brainart16/brenox/internal/db"
	"github.com/brainart16/brenox/internal/storage"
)

var safeFileNamePattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

var (
	ErrNotFound           = errors.New("message not found")
	ErrForbidden          = errors.New("permission denied")
	ErrStorageUnavailable = errors.New("file storage unavailable")
	ErrInvalidFile        = errors.New("invalid file")
	ErrFileTooLarge       = errors.New("file exceeds maximum size")
	ErrMimeNotAllowed     = errors.New("mime type not allowed")
	ErrObjectNotFound     = errors.New("uploaded object not found")
	ErrObjectKeyMismatch  = errors.New("object key does not belong to user")
)

type ObjectStore interface {
	PresignPut(ctx context.Context, key, contentType string, size int64) (string, time.Time, error)
	PresignGet(ctx context.Context, key string) (string, time.Time, error)
	HeadObject(ctx context.Context, key string) (int64, string, error)
	GetObject(ctx context.Context, key string) (*storage.ObjectBody, error)
}

type ContentOpen struct {
	Body      io.ReadCloser
	FileName  string
	MimeType  string
	SizeBytes int64
}

type VirusScanner interface {
	Scan(ctx context.Context, objectKey string) error
}

type noopScanner struct{}

func (noopScanner) Scan(context.Context, string) error { return nil }

func NewNoopVirusScanner() VirusScanner { return noopScanner{} }

type MessageBroadcaster interface {
	PublishMessageUpdated(workspaceID, channelID int64, payload map[string]any)
}

type ChannelAccessChecker interface {
	AssertChannelAccess(ctx context.Context, workspaceID, channelID, userID int64) error
}

type AttachmentInput struct {
	ObjectKey string
	FileName  string
	MimeType  string
	SizeBytes int64
}

type UploadURLResponse struct {
	ObjectKey string `json:"object_key"`
	UploadURL string `json:"upload_url"`
	ExpiresAt string `json:"expires_at"`
}

type AttachmentResponse struct {
	ID        int64  `json:"id"`
	MessageID int64  `json:"message_id"`
	FileName  string `json:"file_name"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at"`
}

type Service struct {
	queries     *db.Queries
	store       ObjectStore
	scanner     VirusScanner
	broadcaster MessageBroadcaster
	access      ChannelAccessChecker
	maxBytes    int64
}

func NewService(
	queries *db.Queries,
	store ObjectStore,
	scanner VirusScanner,
	broadcaster MessageBroadcaster,
	access ChannelAccessChecker,
) *Service {
	if scanner == nil {
		scanner = NewNoopVirusScanner()
	}
	return &Service{
		queries:     queries,
		store:       store,
		scanner:     scanner,
		broadcaster: broadcaster,
		access:      access,
		maxBytes:    storage.MaxUploadBytes(),
	}
}

func (s *Service) CreateUploadURL(
	ctx context.Context,
	userID int64,
	fileName, mimeType string,
	sizeBytes int64,
) (UploadURLResponse, error) {
	if s.store == nil {
		return UploadURLResponse{}, ErrStorageUnavailable
	}
	if err := validateUpload(fileName, mimeType, sizeBytes, s.maxBytes); err != nil {
		return UploadURLResponse{}, err
	}

	objectKey := buildObjectKey(userID, fileName)
	url, expires, err := s.store.PresignPut(ctx, objectKey, mimeType, sizeBytes)
	if err != nil {
		return UploadURLResponse{}, err
	}

	return UploadURLResponse{
		ObjectKey: objectKey,
		UploadURL: url,
		ExpiresAt: expires.UTC().Format(time.RFC3339),
	}, nil
}

func (s *Service) AttachToMessage(
	ctx context.Context,
	workspaceID, channelID, messageID, userID int64,
	inputs []AttachmentInput,
) ([]AttachmentResponse, error) {
	if len(inputs) == 0 {
		return nil, ErrInvalidFile
	}
	if err := s.access.AssertChannelAccess(ctx, workspaceID, channelID, userID); err != nil {
		return nil, mapAccessError(err)
	}

	message, err := s.queries.GetMessageInChannel(ctx, db.GetMessageInChannelParams{
		ID:          messageID,
		ChannelID:   channelID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil, ErrNotFound
	}
	if message.SenderID != userID {
		return nil, ErrForbidden
	}

	created := make([]AttachmentResponse, 0, len(inputs))
	for _, input := range inputs {
		item, err := s.createAttachment(ctx, workspaceID, channelID, messageID, userID, input)
		if err != nil {
			return nil, err
		}
		created = append(created, item)
	}

	if s.broadcaster != nil {
		s.broadcaster.PublishMessageUpdated(workspaceID, channelID, s.messageUpdatedPayload(message, created))
	}

	return created, nil
}

func (s *Service) AttachOnMessageCreate(
	ctx context.Context,
	workspaceID, channelID int64,
	message db.Message,
	userID int64,
	inputs []AttachmentInput,
) ([]AttachmentResponse, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	created := make([]AttachmentResponse, 0, len(inputs))
	for _, input := range inputs {
		item, err := s.createAttachment(ctx, workspaceID, channelID, message.ID, userID, input)
		if err != nil {
			return nil, err
		}
		created = append(created, item)
	}

	if s.broadcaster != nil && len(created) > 0 {
		s.broadcaster.PublishMessageUpdated(workspaceID, channelID, s.messageUpdatedPayload(message, created))
	}

	return created, nil
}

func (s *Service) ListByMessage(
	ctx context.Context,
	workspaceID, channelID, messageID, userID int64,
) ([]AttachmentResponse, error) {
	if err := s.access.AssertChannelAccess(ctx, workspaceID, channelID, userID); err != nil {
		return nil, mapAccessError(err)
	}

	if _, err := s.queries.GetMessageInChannel(ctx, db.GetMessageInChannelParams{
		ID:          messageID,
		ChannelID:   channelID,
		WorkspaceID: workspaceID,
	}); err != nil {
		return nil, ErrNotFound
	}

	rows, err := s.queries.ListAttachmentsByMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}

	items := make([]AttachmentResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.toResponse(workspaceID, channelID, row))
	}
	return items, nil
}

func (s *Service) OpenContent(
	ctx context.Context,
	workspaceID, channelID, messageID, attachmentID, userID int64,
) (*ContentOpen, error) {
	if s.store == nil {
		return nil, ErrStorageUnavailable
	}
	if err := s.access.AssertChannelAccess(ctx, workspaceID, channelID, userID); err != nil {
		return nil, mapAccessError(err)
	}

	if _, err := s.queries.GetMessageInChannel(ctx, db.GetMessageInChannelParams{
		ID:          messageID,
		ChannelID:   channelID,
		WorkspaceID: workspaceID,
	}); err != nil {
		return nil, ErrNotFound
	}

	row, err := s.queries.GetAttachmentByID(ctx, attachmentID)
	if err != nil || row.MessageID != messageID {
		return nil, ErrNotFound
	}

	obj, err := s.store.GetObject(ctx, row.ObjectKey)
	if err != nil {
		return nil, ErrObjectNotFound
	}

	mimeType := row.MimeType
	if obj.ContentType != "" {
		mimeType = obj.ContentType
	}
	size := row.SizeBytes
	if obj.Size > 0 {
		size = obj.Size
	}

	return &ContentOpen{
		Body:      obj.Body,
		FileName:  row.FileName,
		MimeType:  mimeType,
		SizeBytes: size,
	}, nil
}

func (s *Service) createAttachment(
	ctx context.Context,
	workspaceID, channelID, messageID, userID int64,
	input AttachmentInput,
) (AttachmentResponse, error) {
	if s.store == nil {
		return AttachmentResponse{}, ErrStorageUnavailable
	}
	if err := validateUpload(input.FileName, input.MimeType, input.SizeBytes, s.maxBytes); err != nil {
		return AttachmentResponse{}, err
	}
	if !strings.HasPrefix(input.ObjectKey, fmt.Sprintf("uploads/%d/", userID)) {
		return AttachmentResponse{}, ErrObjectKeyMismatch
	}

	size, contentType, err := s.store.HeadObject(ctx, input.ObjectKey)
	if err != nil {
		return AttachmentResponse{}, ErrObjectNotFound
	}
	if size != input.SizeBytes {
		return AttachmentResponse{}, ErrInvalidFile
	}
	if contentType != "" && !strings.EqualFold(contentType, input.MimeType) {
		input.MimeType = contentType
	}

	if err := s.scanner.Scan(ctx, input.ObjectKey); err != nil {
		return AttachmentResponse{}, err
	}

	row, err := s.queries.CreateAttachment(ctx, db.CreateAttachmentParams{
		MessageID:  messageID,
		UploaderID: userID,
		ObjectKey:  input.ObjectKey,
		FileName:   input.FileName,
		MimeType:   input.MimeType,
		SizeBytes:  input.SizeBytes,
	})
	if err != nil {
		return AttachmentResponse{}, err
	}

	return s.toResponse(workspaceID, channelID, row), nil
}

func (s *Service) toResponse(workspaceID, channelID int64, row db.Attachment) AttachmentResponse {
	return AttachmentResponse{
		ID:        row.ID,
		MessageID: row.MessageID,
		FileName:  row.FileName,
		MimeType:  row.MimeType,
		SizeBytes: row.SizeBytes,
		URL:       contentURL(workspaceID, channelID, row.MessageID, row.ID),
		CreatedAt: formatTime(row.CreatedAt),
	}
}

func contentURL(workspaceID, channelID, messageID, attachmentID int64) string {
	return fmt.Sprintf(
		"/api/workspaces/%d/channels/%d/messages/%d/attachments/%d/content",
		workspaceID, channelID, messageID, attachmentID,
	)
}

func (s *Service) messageUpdatedPayload(message db.Message, attachments []AttachmentResponse) map[string]any {
	attachmentPayload := make([]map[string]any, 0, len(attachments))
	for _, item := range attachments {
		attachmentPayload = append(attachmentPayload, map[string]any{
			"id":         item.ID,
			"file_name":  item.FileName,
			"mime_type":  item.MimeType,
			"size_bytes": item.SizeBytes,
			"url":        item.URL,
			"created_at": item.CreatedAt,
		})
	}

	return map[string]any{
		"id":          message.ID,
		"channel_id":  message.ChannelID,
		"sender_id":   message.SenderID,
		"content":     message.Content,
		"created_at":  formatTime(message.CreatedAt),
		"attachments": attachmentPayload,
	}
}

func buildObjectKey(userID int64, fileName string) string {
	safeName := safeFileNamePattern.ReplaceAllString(path.Base(fileName), "_")
	if safeName == "" {
		safeName = "file"
	}
	return fmt.Sprintf("uploads/%d/%s/%s", userID, uuid.NewString(), safeName)
}

func mapAccessError(err error) error {
	return err
}

func formatTime(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return ts.Time.UTC().Format(time.RFC3339)
}
