package sse

import "bytes"

type Decoder struct {
	buffer []byte
}

func (d *Decoder) Feed(chunk []byte) [][]byte {
	var frames [][]byte
	if len(chunk) > 0 {
		d.buffer = append(d.buffer, chunk...)
		end, width := frameEnd(d.buffer)
		for end >= 0 {
			frame := append([]byte(nil), d.buffer[:end+width]...)
			frames = append(frames, frame)
			d.buffer = append(d.buffer[:0], d.buffer[end+width:]...)
			end, width = frameEnd(d.buffer)
		}
	}
	return frames
}

func (d *Decoder) Flush() []byte {
	var out []byte
	if len(bytes.TrimSpace(d.buffer)) > 0 {
		out = append([]byte(nil), d.buffer...)
	}
	d.buffer = nil
	return out
}

func frameEnd(raw []byte) (int, int) {
	lf := bytes.Index(raw, []byte("\n\n"))
	crlf := bytes.Index(raw, []byte("\r\n\r\n"))
	end, width := crlf, 4
	switch {
	case lf < 0 && crlf < 0:
		end, width = -1, 0
	case lf >= 0 && (crlf < 0 || lf < crlf):
		end, width = lf, 2
	}
	return end, width
}
