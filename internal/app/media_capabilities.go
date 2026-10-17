package app

import (
	"context"
	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

func packetDropHelpAvailable(data []byte) bool { return localmedia.PacketDropHelpAvailable(data) }
func supportsAudioPacketDrop(ctx context.Context, c Config) (bool, error) {
	available, err := localmedia.SupportsAudioPacketDrop(ctx, c)
	return available, localMediaError(err)
}
func supportsAudioPacketDropWithProbe(ctx context.Context, c Config, query func(context.Context, string, ...string) ([]byte, error)) (bool, error) {
	available, err := localmedia.SupportsAudioPacketDropWithProbe(ctx, c, query)
	return available, localMediaError(err)
}
