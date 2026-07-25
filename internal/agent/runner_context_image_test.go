package agent

import (
	"strings"
	"testing"

	"autoto/internal/db"
	"autoto/internal/media"
	"autoto/internal/providers"
)

func TestAttachmentBlocksPreferServerNormalizedModelData(t *testing.T) {
	modelData := agentTestPNG(t, 2, 1)
	original := []byte("untrusted original bytes")
	blocks := attachmentBlocks(db.Message{Attachments: []db.Attachment{{
		Filename:         "photo.jpg",
		MIMEType:         "image/jpeg",
		Kind:             "image",
		Data:             original,
		ModelData:        modelData,
		ModelMIME:        media.MIMEPNG,
		Width:            2,
		Height:           1,
		ProcessingStatus: media.ProcessingReady,
	}}})
	if len(blocks) != 1 || blocks[0].Type != "image" || blocks[0].MIMEType != media.MIMEPNG || blocks[0].Width != 2 || blocks[0].Height != 1 {
		t.Fatalf("unexpected normalized image block: %+v", blocks)
	}
	if string(blocks[0].Data) != string(modelData) || string(blocks[0].Data) == string(original) {
		t.Fatal("attachment block did not prefer normalized model data")
	}
}

func TestAttachmentBlocksDowngradeRejectedAndLegacyCorruptImages(t *testing.T) {
	for _, attachment := range []db.Attachment{
		{Filename: "too-large.png", MIMEType: media.MIMEPNG, Kind: "image", ProcessingStatus: media.ProcessingRejected, ProcessingError: "图片像素数超过限制。", Data: agentTestPNG(t, 1, 1)},
		{Filename: "corrupt.png", MIMEType: media.MIMEPNG, Kind: "image", Data: append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("broken")...)},
	} {
		blocks := attachmentBlocks(db.Message{Attachments: []db.Attachment{attachment}})
		if len(blocks) != 1 || blocks[0].Type != "text" || !strings.Contains(blocks[0].Text, attachment.Filename) || !strings.Contains(blocks[0].Text, "未作为图片发送") {
			t.Fatalf("expected explicit image downgrade for %+v, got %+v", attachment, blocks)
		}
	}
}

func TestAttachmentBlocksRevalidateLegacyImageBytes(t *testing.T) {
	original := agentTestPNG(t, 3, 2)
	blocks := attachmentBlocks(db.Message{Attachments: []db.Attachment{{Filename: "legacy.png", MIMEType: media.MIMEPNG, Kind: "image", Data: original}}})
	if len(blocks) != 1 || blocks[0].Type != "image" || blocks[0].MIMEType != media.MIMEPNG || blocks[0].Width != 3 || blocks[0].Height != 2 {
		t.Fatalf("unexpected legacy normalized block: %+v", blocks)
	}
	if len(blocks[0].Data) == 0 || &blocks[0].Data[0] == &original[0] {
		t.Fatal("legacy image bytes were forwarded without independent normalization")
	}
}

func TestProviderMediaBudgetRetainsNewestImagesAndDowngradesOlderHistory(t *testing.T) {
	messages := make([]providers.Message, maxProviderImageBlocks+2)
	for index := range messages {
		messages[index] = providers.Message{Role: "user", Blocks: []providers.ContentBlock{{Type: "image", Filename: string(rune('a'+index)) + ".png", MIMEType: media.MIMEPNG, Data: []byte{byte(index + 1)}, Width: 1, Height: 1}}}
	}
	bounded := enforceProviderMediaBudget(messages)
	binaryCount := 0
	for index, message := range bounded {
		if len(message.Blocks) != 1 {
			t.Fatalf("unexpected blocks at %d: %+v", index, message.Blocks)
		}
		if len(message.Blocks[0].Data) > 0 {
			binaryCount++
			if index < 2 {
				t.Fatalf("old image %d was retained ahead of newer history", index)
			}
		} else if index >= 2 || message.Blocks[0].Type != "text" || !strings.Contains(message.Blocks[0].Text, "历史媒体预算") {
			t.Fatalf("unexpected media downgrade at %d: %+v", index, message.Blocks[0])
		}
	}
	if binaryCount != maxProviderImageBlocks {
		t.Fatalf("expected %d retained images, got %d", maxProviderImageBlocks, binaryCount)
	}
}

func TestEstimateBlockTokensIncludesImageDimensionsAndTransportBytes(t *testing.T) {
	base := estimateBlockTokens(providers.ContentBlock{Type: "image", Filename: "image.png", MIMEType: media.MIMEPNG})
	small := estimateBlockTokens(providers.ContentBlock{Type: "image", Filename: "image.png", MIMEType: media.MIMEPNG, Data: make([]byte, 4096), Width: 128, Height: 128})
	large := estimateBlockTokens(providers.ContentBlock{Type: "image", Filename: "image.png", MIMEType: media.MIMEPNG, Data: make([]byte, 8192), Width: 2048, Height: 1536})
	if small <= base || large <= small {
		t.Fatalf("expected monotonic image token estimates: base=%d small=%d large=%d", base, small, large)
	}
}
