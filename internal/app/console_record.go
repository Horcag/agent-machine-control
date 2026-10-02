package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/png"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

type ConsoleRecordRequest struct {
	Target         string `json:"target,omitempty"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	Frames         int    `json:"frames"`
	IntervalMillis int    `json:"interval_millis"`
}

type ConsoleRecording struct {
	MIMEType   string      `json:"mime_type"`
	SHA256     string      `json:"sha256"`
	Width      int         `json:"width"`
	Height     int         `json:"height"`
	ObservedAt []time.Time `json:"observed_at"`
	Data       []byte      `json:"data,omitempty"`
}

// Record produces a bounded animated GIF from VM frames, without a host recorder.
func (s *ConsoleService) Record(ctx context.Context, actor domain.ActorContext, req ConsoleRecordRequest) (ConsoleRecording, error) {
	var out ConsoleRecording
	if req.Frames < 2 || req.Frames > 30 || req.IntervalMillis < 100 || req.IntervalMillis > 2000 || req.Frames*req.IntervalMillis > 30000 || req.Width < 1 || req.Height < 1 || req.Width > 307200/req.Height {
		return out, errors.New("app: invalid recording bounds")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	animation, observed, err := s.collectRecordingFrames(ctx, actor, req)
	if err != nil {
		return out, err
	}
	out.ObservedAt = observed

	var buffer bytes.Buffer
	if err := gif.EncodeAll(&buffer, &animation); err != nil {
		return out, err
	}
	if buffer.Len() > 20*1024*1024 {
		return out, errors.New("app: recording exceeds artifact limit")
	}
	digest := sha256.Sum256(buffer.Bytes())
	out.Data = buffer.Bytes()
	out.SHA256 = hex.EncodeToString(digest[:])
	out.MIMEType = "image/gif"
	out.Width = req.Width
	out.Height = req.Height
	return out, nil
}

func waitRecordingFrame(ctx context.Context, intervalMillis int) error {
	timer := time.NewTimer(time.Duration(intervalMillis) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func setRecordingDelay(animation *gif.GIF, observed []time.Time, index int) error {
	delay := max(1, int(observed[index].Sub(observed[index-1]).Milliseconds()/10))
	if delay > 65535 {
		return errors.New("app: invalid recording frame timing")
	}
	animation.Delay[index-1] = delay
	return nil
}

func recordingPalettedFrame(data []byte) (*image.Paletted, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	p := image.NewPaletted(img.Bounds(), palette.Plan9)
	draw.Draw(p, p.Bounds(), img, img.Bounds().Min, draw.Src)
	return p, nil
}

func (s *ConsoleService) collectRecordingFrames(ctx context.Context, actor domain.ActorContext, req ConsoleRecordRequest) (gif.GIF, []time.Time, error) {
	animation := gif.GIF{LoopCount: 0}
	observed := make([]time.Time, 0, req.Frames)
	for i := 0; i < req.Frames; i++ {
		if err := ctx.Err(); err != nil {
			return animation, observed, err
		}
		frame, err := s.Screenshot(ctx, actor, ConsoleScreenshotRequest{Target: req.Target, Width: req.Width, Height: req.Height})
		if err != nil {
			return animation, observed, err
		}
		p, err := recordingPalettedFrame(frame.Data)
		if err != nil {
			return animation, observed, err
		}

		animation.Image = append(animation.Image, p)
		animation.Delay = append(animation.Delay, req.IntervalMillis/10)
		observed = append(observed, frame.ObservedAt)
		if i > 0 {
			if err := setRecordingDelay(&animation, observed, i); err != nil {
				return animation, observed, err
			}
		}

		if i < req.Frames-1 {
			if err := waitRecordingFrame(ctx, req.IntervalMillis); err != nil {
				return animation, observed, err
			}
		}

	}
	return animation, observed, nil
}
