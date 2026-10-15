package sse

import "bytes"

// Decoder incrementally extracts SSE frames and normalizes line endings to LF.
type Decoder struct {
	buffer []byte
	skipLF bool
}

// Feed adds a chunk and returns complete frames with line endings normalized to LF.
func (d *Decoder) Feed(chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return nil
	}
	for _, value := range chunk {
		if d.skipLF {
			d.skipLF = false
			if value == '\n' {
				continue
			}
		}
		if value == '\r' {
			d.buffer = append(d.buffer, '\n')
			d.skipLF = true
		} else {
			d.buffer = append(d.buffer, value)
		}
	}
	var frames [][]byte
	for {
		end, width := frameEnd(d.buffer)
		if end < 0 {
			break
		}
		frame := append([]byte(nil), d.buffer[:end+width]...)
		frames = append(frames, frame)
		d.buffer = append(d.buffer[:0], d.buffer[end+width:]...)
	}
	return frames
}

// Flush returns any non-whitespace trailing bytes and resets the decoder.
func (d *Decoder) Flush() []byte {
	d.skipLF = false
	if len(bytes.TrimSpace(d.buffer)) == 0 {
		d.buffer = nil
		return nil
	}
	out := append([]byte(nil), d.buffer...)
	d.buffer = nil
	return out
}

func frameEnd(raw []byte) (int, int) {
	end := bytes.Index(raw, []byte("\n\n"))
	if end < 0 {
		return -1, 0
	}
	return end, 2
}
