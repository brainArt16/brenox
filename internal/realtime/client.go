package realtime

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/brainart16/brenox/internal/calls"
	"github.com/brainart16/brenox/internal/chat"
	"github.com/brainart16/brenox/internal/httperr"
	"github.com/gorilla/websocket"
)

type Client struct {
	conn        *websocket.Conn
	userID      int64
	channelID   int64
	workspaceID int64
	remoteIP    string
	hub         *Hub
	chat        *chat.Service
	calls       *calls.Service
	send        chan Event
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		var event Event
		err := c.conn.ReadJSON(&event)
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("websocket read error", "user_id", c.userID, "error", err)
			}
			break
		}

		switch event.Type {
		case "message.send":
			c.handleMessageSend(event)
		case "typing.start":
			c.broadcastTyping("typing.start")
		case "typing.stop":
			c.broadcastTyping("typing.stop")
		case "call.offer", "call.answer", "call.ice":
			c.handleCallSignal(event)
		case "call.video.on", "call.video.off",
			"call.screen.start", "call.screen.stop",
			"call.speaker.changed",
			"call.recording.start", "call.recording.stop",
			"call.preferences":
			c.handleCallMediaEvent(event)
		default:
			slog.Info("ignored websocket event", "type", event.Type, "user_id", c.userID)
		}
	}
}

func (c *Client) handleMessageSend(event Event) {
	content, ok := parseMessageContent(event.Payload)
	if !ok {
		c.sendClientError("invalid message payload")
		return
	}

	if _, err := c.chat.SendMessage(
		context.Background(),
		c.workspaceID,
		c.channelID,
		c.userID,
		content,
		nil,
		nil,
	); err != nil {
		c.handleSendMessageError(err)
	}
	// message.new is broadcast by the chat service for all send paths.
}

func (c *Client) handleSendMessageError(err error) {
	switch {
	case errors.Is(err, chat.ErrNotMember):
		c.sendClientError("not a channel member")
	case errors.Is(err, chat.ErrNotWorkspaceMember):
		c.sendClientError("not a workspace member")
	case errors.Is(err, chat.ErrChannelNotFound):
		c.sendClientError("channel not found")
	case errors.Is(err, chat.ErrForbidden):
		c.sendClientError("permission denied")
	case errors.Is(err, chat.ErrEmptyContent), errors.Is(err, chat.ErrMessageTooLong):
		c.sendClientError(httperr.Sanitize(err.Error()))
	default:
		slog.Error("message.send failed", "user_id", c.userID, "error", err)
		c.sendClientError(httperr.InternalMessage)
	}
}

func (c *Client) handleCallSignal(event Event) {
	if c.calls == nil {
		c.sendClientError("calls unavailable")
		return
	}

	callID := payloadInt64(event.Payload, "call_id")
	if callID == 0 {
		c.sendClientError("invalid call signal payload")
		return
	}

	ctx, err := c.calls.ValidateSignal(context.Background(), callID, c.userID)
	if err != nil {
		c.sendCallError(err)
		return
	}

	if ctx.ChannelID != c.channelID || ctx.WorkspaceID != c.workspaceID {
		c.sendClientError("call channel mismatch")
		return
	}

	payload := withFromUser(event.Payload, c.userID)
	c.hub.Publish(NewOutboundEvent(event.Type, c.workspaceID, c.channelID, payload))
}

func (c *Client) handleCallMediaEvent(event Event) {
	if c.calls == nil {
		c.sendClientError("calls unavailable")
		return
	}

	callID := payloadInt64(event.Payload, "call_id")
	if callID == 0 {
		c.sendClientError("invalid call media payload")
		return
	}

	ctx, err := c.calls.ValidateSignal(context.Background(), callID, c.userID)
	if err != nil {
		c.sendCallError(err)
		return
	}

	if ctx.ChannelID != c.channelID || ctx.WorkspaceID != c.workspaceID {
		c.sendClientError("call channel mismatch")
		return
	}

	payload := withFromUser(event.Payload, c.userID)

	switch event.Type {
	case "call.recording.start":
		metadata, _ := payloadMap(event.Payload)
		recording, err := c.calls.StartRecording(context.Background(), callID, c.userID, metadata)
		if err != nil {
			c.sendCallError(err)
			return
		}
		payload["recording_id"] = recording.ID
		payload["started_at"] = recording.StartedAt
	case "call.recording.stop":
		recordingID := payloadInt64(event.Payload, "recording_id")
		if recordingID == 0 {
			c.sendClientError("recording_id required")
			return
		}
		recording, err := c.calls.StopRecording(context.Background(), callID, c.userID, recordingID)
		if err != nil {
			c.sendCallError(err)
			return
		}
		payload["recording_id"] = recording.ID
		payload["ended_at"] = recording.EndedAt
	}

	c.hub.Publish(NewOutboundEvent(event.Type, c.workspaceID, c.channelID, payload))
}

func payloadMap(payload any) (map[string]any, bool) {
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return map[string]any{}, false
	}
	cloned := make(map[string]any, len(payloadMap))
	for key, value := range payloadMap {
		switch key {
		case "call_id", "recording_id", "to_user_id":
			continue
		default:
			cloned[key] = value
		}
	}
	return cloned, true
}

func (c *Client) broadcastTyping(eventType string) {
	c.hub.Publish(NewOutboundEvent(eventType, c.workspaceID, c.channelID, map[string]any{
		"user_id": c.userID,
	}))
}

func (c *Client) sendClientError(message string) {
	event := NewOutboundEvent("error", c.workspaceID, c.channelID, map[string]any{
		"message": httperr.Sanitize(message),
	})
	select {
	case c.send <- event:
	default:
		slog.Warn("client error dropped, send buffer full", "user_id", c.userID)
	}
}

func (c *Client) sendCallError(err error) {
	message := httperr.ClientMessage(err,
		calls.ErrNotFound,
		calls.ErrNotMember,
		calls.ErrNotParticipant,
		calls.ErrCallEnded,
		calls.ErrCallAlreadyActive,
		calls.ErrCallFull,
		calls.ErrChannelNotFound,
		calls.ErrRecordingActive,
		calls.ErrRecordingNotFound,
		calls.ErrInvalidMode,
	)
	if message == httperr.InternalMessage {
		slog.Error("call websocket error", "user_id", c.userID, "error", err)
	}
	c.sendClientError(message)
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case event, ok := <-c.send:
			if !ok {
				return
			}

			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteJSON(event); err != nil {
				slog.Warn("websocket write error", "user_id", c.userID, "error", err)
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				slog.Warn("websocket ping error", "user_id", c.userID, "error", err)
				return
			}
			c.hub.touchPresence(c.userID)
		}
	}
}

func parseMessageContent(payload any) (string, bool) {
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return "", false
	}

	content, ok := payloadMap["content"].(string)
	if !ok {
		return "", false
	}

	return content, true
}
