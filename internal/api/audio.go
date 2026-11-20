package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os/exec"
	"time"
)

type audioFormat struct {
	mime    string
	muxer   string
	encoder string
}

const sampleRate = 24000

var audioFormats = map[string]audioFormat{
	"mp3":  {mime: "audio/mpeg"},
	"opus": {mime: "audio/ogg", muxer: "ogg", encoder: "libopus"},
	"aac":  {mime: "audio/aac", muxer: "adts", encoder: "aac"},
	"flac": {mime: "audio/flac", muxer: "flac", encoder: "flac"},
	"wav":  {mime: "audio/wav", muxer: "s16le", encoder: "pcm_s16le"},
	"pcm":  {mime: "application/octet-stream", muxer: "s16le", encoder: "pcm_s16le"},
}

func (f audioFormat) converter() (string, error) {
	if f.muxer == "" {
		return "", nil
	}
	return exec.LookPath("ffmpeg")
}

func (f audioFormat) convert(ctx context.Context, ffmpeg string, mp3 []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "mp3", "-i", "pipe:0",
		"-map", "0:a:0", "-ar", fmt.Sprint(sampleRate), "-ac", "1", "-c:a", f.encoder, "-f", f.muxer, "pipe:1")
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = bytes.NewReader(mp3)
	audio, err := cmd.Output()
	if exit, ok := err.(*exec.ExitError); ok {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, exit.Stderr)
	}
	if err != nil {
		return nil, err
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("ffmpeg returned no audio")
	}
	if f.mime == "audio/wav" {
		// FFmpeg cannot finalize a WAV header on a pipe; write the known sizes.
		audio = wrapWAV(audio)
	}
	return audio, nil
}

func wrapWAV(pcm []byte) []byte {
	var header [44]byte
	copy(header[:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(pcm)))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16) // PCM format chunk size
	binary.LittleEndian.PutUint16(header[20:], 1)  // PCM format code
	binary.LittleEndian.PutUint16(header[22:], 1)  // mono
	binary.LittleEndian.PutUint32(header[24:], sampleRate)
	binary.LittleEndian.PutUint32(header[28:], sampleRate*2) // bytes per second
	binary.LittleEndian.PutUint16(header[32:], 2)            // bytes per frame
	binary.LittleEndian.PutUint16(header[34:], 16)           // bits per sample
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(pcm)))
	return append(header[:], pcm...)
}
