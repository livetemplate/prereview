package main

import (
	"fmt"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// dumpGIF writes every frame of a captured GIF to <out>/<name>-NN.png, so each
// frame can be looked at before the GIF ships: a flow can exit 0 and still
// drop or repeat a frame. Each frame is a full image (encodeGIF writes whole
// frames), so no compositing is needed.
func dumpGIF(path, out string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for i, frame := range g.Image {
		p := filepath.Join(out, fmt.Sprintf("%s-%02d.png", name, i))
		w, err := os.Create(p)
		if err != nil {
			return err
		}
		err = png.Encode(w, frame)
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
	}
	fmt.Printf("%s: %d frames, delays %v (1/100 s) → %s\n", path, len(g.Image), g.Delay, out)
	return nil
}
