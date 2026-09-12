package playwright

import (
	"fmt"
	"os"
	"path/filepath"

	ffmpeg "github.com/u2takey/ffmpeg-go"
)

// EncodeVideo assembles the numbered PNG frames in framesDir (frame_%06d.png,
// starting at 1) into an H.264 mp4 at outPath, played back at fps. The output
// directory is created if needed. Requires ffmpeg on PATH.
func EncodeVideo(framesDir, outPath string, fps int) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("creating recording dir: %w", err)
	}

	pattern := filepath.Join(framesDir, "frame_%06d.png")
	err := ffmpeg.Input(pattern, ffmpeg.KwArgs{"framerate": fps, "start_number": 1}).
		Output(outPath, ffmpeg.KwArgs{"c:v": "libx264", "pix_fmt": "yuv420p"}).
		OverWriteOutput().
		Run()
	if err != nil {
		return fmt.Errorf("encoding video: %w", err)
	}

	return nil
}
