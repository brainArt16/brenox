package attachments

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/brainart16/brenox/internal/httperr"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

type createUploadRequest struct {
	FileName  string `json:"file_name" binding:"required"`
	MimeType  string `json:"mime_type" binding:"required"`
	SizeBytes int64  `json:"size_bytes" binding:"required"`
}

func (h *Handler) CreateUpload(c *gin.Context) {
	var req createUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	userID := c.MustGet("user_id").(int64)
	resp, err := h.service.CreateUploadURL(c.Request.Context(), userID, req.FileName, req.MimeType, req.SizeBytes)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

type attachRequest struct {
	ObjectKey string `json:"object_key" binding:"required"`
	FileName  string `json:"file_name" binding:"required"`
	MimeType  string `json:"mime_type" binding:"required"`
	SizeBytes int64  `json:"size_bytes" binding:"required"`
}

func (h *Handler) AttachToMessage(c *gin.Context) {
	workspaceID, err := parseWorkspaceID(c)
	if err != nil {
		return
	}
	channelID, err := parseChannelID(c)
	if err != nil {
		return
	}
	messageID, err := parseMessageID(c)
	if err != nil {
		return
	}

	var body struct {
		Attachments []attachRequest `json:"attachments" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	inputs := make([]AttachmentInput, 0, len(body.Attachments))
	for _, item := range body.Attachments {
		inputs = append(inputs, AttachmentInput{
			ObjectKey: item.ObjectKey,
			FileName:  item.FileName,
			MimeType:  item.MimeType,
			SizeBytes: item.SizeBytes,
		})
	}

	userID := c.MustGet("user_id").(int64)
	items, err := h.service.AttachToMessage(c.Request.Context(), workspaceID, channelID, messageID, userID, inputs)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"attachments": items})
}

func (h *Handler) ListByMessage(c *gin.Context) {
	workspaceID, err := parseWorkspaceID(c)
	if err != nil {
		return
	}
	channelID, err := parseChannelID(c)
	if err != nil {
		return
	}
	messageID, err := parseMessageID(c)
	if err != nil {
		return
	}

	userID := c.MustGet("user_id").(int64)
	items, err := h.service.ListByMessage(c.Request.Context(), workspaceID, channelID, messageID, userID)
	if err != nil {
		writeError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"attachments": items})
}

func (h *Handler) DownloadContent(c *gin.Context) {
	workspaceID, err := parseWorkspaceID(c)
	if err != nil {
		return
	}
	channelID, err := parseChannelID(c)
	if err != nil {
		return
	}
	messageID, err := parseMessageID(c)
	if err != nil {
		return
	}
	attachmentID, err := parseAttachmentID(c)
	if err != nil {
		return
	}

	userID := c.MustGet("user_id").(int64)
	content, err := h.service.OpenContent(
		c.Request.Context(),
		workspaceID,
		channelID,
		messageID,
		attachmentID,
		userID,
	)
	if err != nil {
		writeError(c, err)
		return
	}
	defer content.Body.Close()

	fileName := path.Base(content.FileName)
	if fileName == "." || fileName == "/" {
		fileName = "file"
	}
	safeName := strings.ReplaceAll(fileName, `"`, "")

	c.Header("Content-Type", content.MimeType)
	c.Header("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, safeName))
	c.Header("Cache-Control", "private, max-age=300")
	if content.SizeBytes > 0 {
		c.Header("Content-Length", strconv.FormatInt(content.SizeBytes, 10))
	}
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, content.Body)
}

func parseWorkspaceID(c *gin.Context) (int64, error) {
	workspaceID, err := strconv.ParseInt(c.Param("workspace_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid workspace id"})
		return 0, err
	}
	return workspaceID, nil
}

func parseChannelID(c *gin.Context) (int64, error) {
	channelID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel id"})
		return 0, err
	}
	return channelID, nil
}

func parseMessageID(c *gin.Context) (int64, error) {
	messageID, err := strconv.ParseInt(c.Param("message_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid message id"})
		return 0, err
	}
	return messageID, nil
}

func parseAttachmentID(c *gin.Context) (int64, error) {
	attachmentID, err := strconv.ParseInt(c.Param("attachment_id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid attachment id"})
		return 0, err
	}
	return attachmentID, nil
}

func writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": httperr.Sanitize(err.Error())})
	case errors.Is(err, ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": httperr.Sanitize(err.Error())})
	case errors.Is(err, ErrStorageUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": httperr.Sanitize(err.Error())})
	case errors.Is(err, ErrInvalidFile), errors.Is(err, ErrFileTooLarge), errors.Is(err, ErrMimeNotAllowed), errors.Is(err, ErrObjectNotFound), errors.Is(err, ErrObjectKeyMismatch):
		c.JSON(http.StatusBadRequest, gin.H{"error": httperr.Sanitize(err.Error())})
	default:
		httperr.WriteInternal(c, err)
	}
}
