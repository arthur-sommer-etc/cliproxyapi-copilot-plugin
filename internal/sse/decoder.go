package sse

import "bytes"

type Decoder struct {
	buffer []byte
}

func (d *Decoder) Feed(chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return nil
	}
	d.buffer = append(d.buffer, chunk...)
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

func (d *Decoder) Flush() []byte {
	if len(bytes.TrimSpace(d.buffer)) == 0 {
		d.buffer = nil
		return nil
	}
	out := append([]byte(nil), d.buffer...)
	d.buffer = nil
	return out
}

func frameEnd(raw []byte) (int, int) {
	lf := bytes.Index(raw, []byte("\n\n"))
	crlf := bytes.Index(raw, []byte("\r\n\r\n"))
	switch {
	case lf < 0:
		if crlf < 0 {
			return -1, 0
		}
		return crlf, 4
	case crlf < 0 || lf < crlf:
		return lf, 2
	default:
		return crlf, 4
	}
}
