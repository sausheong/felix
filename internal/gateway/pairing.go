package gateway

import (
	"context"
	"fmt"

	"github.com/sausheong/felix/internal/channel"
)

// PairingBridge exposes WhatsApp pairing operations to the HTTP layer.
type PairingBridge interface {
	StartWhatsAppPairing(ctx context.Context, cb func(channel.WhatsAppQREvent)) error
	WhatsAppStatus() string
	DisconnectWhatsApp(ctx context.Context) error
}

// StartWhatsAppPairing initiates QR pairing on the registered WhatsApp
// channel. Only one pairing session may be active at a time.
func (cm *ChannelManager) StartWhatsAppPairing(ctx context.Context, cb func(channel.WhatsAppQREvent)) error {
	cm.mu.RLock()
	ch, ok := cm.channels["whatsapp"]
	cm.mu.RUnlock()

	if !ok {
		return fmt.Errorf("whatsapp channel not configured")
	}
	waChan, ok := ch.(*channel.WhatsAppChannel)
	if !ok {
		return fmt.Errorf("whatsapp channel has unexpected type")
	}

	switch waChan.Status() {
	case channel.StatusConnecting:
		return fmt.Errorf("whatsapp pairing already in progress")
	case channel.StatusConnected:
		return fmt.Errorf("whatsapp already connected")
	}

	waChan.SetQRCallback(cb)

	// Run Connect in a goroutine so the SSE handler can stream events.
	go func() {
		if err := waChan.Connect(ctx); err != nil {
			cb(channel.WhatsAppQREvent{Type: "error", Err: err.Error()})
			return
		}
		// On success, kick off message processing for this newly-connected channel.
		cm.wg.Add(1)
		go cm.processChannel(ctx, waChan)
	}()
	return nil
}

// WhatsAppStatus returns one of: "not_configured", "not_paired",
// "pairing", "connected", "disconnected".
func (cm *ChannelManager) WhatsAppStatus() string {
	cm.mu.RLock()
	ch, ok := cm.channels["whatsapp"]
	cm.mu.RUnlock()

	if !ok {
		return "not_configured"
	}
	waChan, ok := ch.(*channel.WhatsAppChannel)
	if !ok {
		return "not_configured"
	}

	switch waChan.Status() {
	case channel.StatusConnected:
		return "connected"
	case channel.StatusConnecting:
		return "pairing"
	default:
		if waChan.IsPaired() {
			return "disconnected"
		}
		return "not_paired"
	}
}

// DisconnectWhatsApp unpairs the WhatsApp device.
func (cm *ChannelManager) DisconnectWhatsApp(ctx context.Context) error {
	cm.mu.RLock()
	ch, ok := cm.channels["whatsapp"]
	cm.mu.RUnlock()

	if !ok {
		return fmt.Errorf("whatsapp channel not configured")
	}
	waChan, ok := ch.(*channel.WhatsAppChannel)
	if !ok {
		return fmt.Errorf("whatsapp channel has unexpected type")
	}
	return waChan.Unpair(ctx)
}

// Compile-time check.
var _ PairingBridge = (*ChannelManager)(nil)
