package agent

import (
	"encoding/base64"
	"fmt"
	"net/http"
)

// Image is pi's ImageContent wire format. Data contains raw base64, not a URL.
type Image struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

const (
	maxImages          = 4
	maxImageBytes      = 5 * 1024 * 1024
	maxTotalImageBytes = 10 * 1024 * 1024
)

// Enforce limits before starting an agent or writing to its RPC pipe. Check
// bytes as well as the declared MIME so arbitrary files cannot become images.
func validateImages(images []Image) error {
	if len(images) > maxImages {
		return fmt.Errorf("attach at most %d images", maxImages)
	}
	total := 0
	for i, img := range images {
		if img.Type != "image" {
			return fmt.Errorf("attachment %d is not an image", i+1)
		}
		switch img.MimeType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return fmt.Errorf("image %d: use PNG, JPEG, GIF or WebP", i+1)
		}
		if len(img.Data) > base64.StdEncoding.EncodedLen(maxImageBytes) {
			return fmt.Errorf("image %d exceeds 5 MB", i+1)
		}
		data, err := base64.StdEncoding.Strict().DecodeString(img.Data)
		if err != nil || len(data) == 0 {
			return fmt.Errorf("image %d has invalid base64 data", i+1)
		}
		if len(data) > maxImageBytes {
			return fmt.Errorf("image %d exceeds 5 MB", i+1)
		}
		total += len(data)
		if total > maxTotalImageBytes {
			return fmt.Errorf("images exceed 10 MB in total")
		}
		if http.DetectContentType(data) != img.MimeType {
			return fmt.Errorf("image %d does not match its declared format", i+1)
		}
	}
	return nil
}
