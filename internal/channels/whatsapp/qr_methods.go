package whatsapp

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	goclawprotocol "github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

const qrSessionTimeout = 3 * time.Minute

// cancelEntry wraps a CancelFunc so it can be stored in sync.Map.CompareAndDelete.
type cancelEntry struct {
	cancel context.CancelFunc
}

// QRMethods handles whatsapp.qr.start — delivers bridge QR codes to the UI wizard.
// The QR code comes from the external bridge (Baileys), not generated in-process.
type QRMethods struct {
	instanceStore  store.ChannelInstanceStore
	manager        *channels.Manager
	msgBus         *bus.MessageBus
	activeSessions sync.Map // instanceID (string) -> *cancelEntry
}

func NewQRMethods(instanceStore store.ChannelInstanceStore, manager *channels.Manager, msgBus *bus.MessageBus) *QRMethods {
	return &QRMethods{instanceStore: instanceStore, manager: manager, msgBus: msgBus}
}

func (m *QRMethods) Register(router *gateway.MethodRouter) {
	router.Register(goclawprotocol.MethodWhatsAppQRStart, m.handleQRStart)
}

func (m *QRMethods) handleQRStart(ctx context.Context, client *gateway.Client, req *goclawprotocol.RequestFrame) {
	var params struct {
		InstanceID  string `json:"instance_id"`
		ForceReauth bool   `json:"force_reauth"` // if true, logout current session and generate fresh QR
	}
	if req.Params != nil {
		_ = json.Unmarshal(req.Params, &params)
	}

	instID, err := uuid.Parse(params.InstanceID)
	if err != nil {
		client.SendResponse(goclawprotocol.NewErrorResponse(req.ID, goclawprotocol.ErrInvalidRequest, "invalid instance_id"))
		return
	}

	inst, err := m.instanceStore.Get(ctx, instID)
	if err != nil || inst.ChannelType != channels.TypeWhatsApp {
		client.SendResponse(goclawprotocol.NewErrorResponse(req.ID, goclawprotocol.ErrNotFound, "whatsapp instance not found"))
		return
	}

	qrCtx, cancel := context.WithTimeout(ctx, qrSessionTimeout)
	entry := &cancelEntry{cancel: cancel}

	// Cancel any previous QR session for this instance so the user can retry.
	if prev, loaded := m.activeSessions.Swap(params.InstanceID, entry); loaded {
		if prevEntry, ok := prev.(*cancelEntry); ok {
			prevEntry.cancel()
		}
	}

	// ACK immediately — QR/done events arrive asynchronously.
	client.SendResponse(goclawprotocol.NewOKResponse(req.ID, map[string]any{"status": "started"}))

	go m.runQRSession(qrCtx, entry, client, params.InstanceID, inst.Name, params.ForceReauth)
}

func (m *QRMethods) runQRSession(ctx context.Context, entry *cancelEntry, client *gateway.Client, instanceIDStr, channelName string, forceReauth bool) {
	defer entry.cancel()
	defer m.activeSessions.CompareAndDelete(instanceIDStr, entry)

	if ch, ok := m.manager.GetChannel(channelName); ok {
		if wa, ok := ch.(*Channel); ok {
			if wa.IsAuthenticated() && !forceReauth {
				// Already authenticated and caller didn't request re-link — signal "connected" to UI.
				client.SendEvent(goclawprotocol.EventFrame{
					Type:  goclawprotocol.FrameTypeEvent,
					Event: goclawprotocol.EventWhatsAppQRDone,
					Payload: map[string]any{
						"instance_id":       instanceIDStr,
						"success":           true,
						"already_connected": true,
					},
				})
				slog.Info("whatsapp QR skipped — already authenticated", "instance", instanceIDStr)
				return
			}
			if forceReauth || wa.GetLastQRB64() == "" {
				// Force re-link or no cached QR: ask bridge to logout and restart auth.
				if err := wa.SendBridgeCommand("reauth"); err != nil {
					slog.Warn("whatsapp QR: failed to send reauth to bridge", "instance", instanceIDStr, "error", err)
				} else {
					slog.Info("whatsapp QR: sent reauth to bridge", "instance", instanceIDStr, "force", forceReauth)
				}
			} else {
				// Deliver cached QR if bridge sent it before the wizard opened.
				client.SendEvent(goclawprotocol.EventFrame{
					Type:  goclawprotocol.FrameTypeEvent,
					Event: goclawprotocol.EventWhatsAppQRCode,
					Payload: map[string]any{
						"instance_id": instanceIDStr,
						"png_b64":     wa.GetLastQRB64(),
					},
				})
			}
		}
	}

	// Subscribe to bus events for this channel's QR lifecycle.
	subID := "whatsapp-qr-" + instanceIDStr
	done := make(chan struct{})

	m.msgBus.Subscribe(subID, func(event bus.Event) {
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			return
		}
		if payload["channel_name"] != channelName {
			return
		}

		switch event.Name {
		case goclawprotocol.EventWhatsAppQRCode:
			client.SendEvent(goclawprotocol.EventFrame{
				Type:  goclawprotocol.FrameTypeEvent,
				Event: goclawprotocol.EventWhatsAppQRCode,
				Payload: map[string]any{
					"instance_id": instanceIDStr,
					"png_b64":     payload["png_b64"],
				},
			})

		case goclawprotocol.EventWhatsAppQRDone:
			client.SendEvent(goclawprotocol.EventFrame{
				Type:  goclawprotocol.FrameTypeEvent,
				Event: goclawprotocol.EventWhatsAppQRDone,
				Payload: map[string]any{
					"instance_id": instanceIDStr,
					"success":     true,
				},
			})
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	defer m.msgBus.Unsubscribe(subID)

	select {
	case <-done:
		slog.Info("whatsapp QR session completed", "instance", instanceIDStr)
	case <-ctx.Done():
		client.SendEvent(goclawprotocol.EventFrame{
			Type:  goclawprotocol.FrameTypeEvent,
			Event: goclawprotocol.EventWhatsAppQRDone,
			Payload: map[string]any{
				"instance_id": instanceIDStr,
				"success":     false,
				"error":       "QR session timed out — restart to try again",
			},
		})
		slog.Info("whatsapp QR session timed out", "instance", instanceIDStr)
	}
}

