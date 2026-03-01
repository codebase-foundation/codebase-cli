package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ──────────────────────────────────────────────────────────────
//  Demoscene boot — plasma, 3D cube, pixel logo, copper bars,
//  sine scroller. Half-block (▀) framebuffer with true-color
//  ANSI and color quantization for smooth terminal rendering.
// ──────────────────────────────────────────────────────────────

type demoTickMsg time.Time
type bootTickMsg struct{}
type bootDoneMsg struct{}
type bootAudioMsg struct{ player *AudioPlayer }

type bootStep struct {
	label string
	value string
	done  bool
}

type rgb struct{ r, g, b uint8 }

func (c rgb) quantize() rgb {
	return rgb{(c.r / 8) * 8, (c.g / 8) * 8, (c.b / 8) * 8}
}

// ── Codebase "C" logo (exact match of favicon.svg) ──────────
// SVG: viewBox="7 6 6 7", pixels at (9-11,7) (8,8-10) (9-11,11)
//   .███
//   █...
//   █...
//   █...
//   .███
var logoC = [5][4]bool{
	{false, true, true, true},
	{true, false, false, false},
	{true, false, false, false},
	{true, false, false, false},
	{false, true, true, true},
}

const (
	logoW = 4
	logoH = 5
)

// ── 3D cube ──────────────────────────────────────────────────

type vec3 struct{ x, y, z float64 }
type vec2 struct{ x, y float64 }

var cubeVerts = [8]vec3{
	{-1, -1, -1}, {1, -1, -1}, {1, 1, -1}, {-1, 1, -1},
	{-1, -1, 1}, {1, -1, 1}, {1, 1, 1}, {-1, 1, 1},
}

var cubeEdges = [12][2]int{
	{0, 1}, {1, 2}, {2, 3}, {3, 0},
	{4, 5}, {5, 6}, {6, 7}, {7, 4},
	{0, 4}, {1, 5}, {2, 6}, {3, 7},
}

// ── Plasma palette ──────────────────────────────────────────

var plasmaPalette [256]rgb

func init() {
	for i := 0; i < 256; i++ {
		t := float64(i) / 256.0
		r := math.Sin(t*math.Pi*2)*0.5 + 0.5
		g := math.Sin(t*math.Pi*2+2.094)*0.5 + 0.5
		b := math.Sin(t*math.Pi*2+4.189)*0.5 + 0.5
		avg := (r + g + b) / 3.0
		r = avg + (r-avg)*1.6
		g = avg + (g-avg)*1.6
		b = avg + (b-avg)*1.6
		plasmaPalette[i] = rgb{
			clampU8(r * 200),
			clampU8(g * 200),
			clampU8(b * 200),
		}
	}
}

// ── Bitmap font (5×7) ────────────────────────────────────────

var glyphMap = map[rune][7]uint8{
	'a': {0b01110, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'b': {0b11110, 0b10001, 0b10001, 0b11110, 0b10001, 0b10001, 0b11110},
	'c': {0b01110, 0b11001, 0b10000, 0b10000, 0b10000, 0b11001, 0b01110},
	'd': {0b11100, 0b10110, 0b10011, 0b10001, 0b10011, 0b10110, 0b11100},
	'e': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b11111},
	'f': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b10000},
	'g': {0b01110, 0b10001, 0b10000, 0b10111, 0b10001, 0b10001, 0b01110},
	'h': {0b10001, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'i': {0b01110, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'l': {0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b11111},
	'm': {0b10001, 0b11011, 0b10101, 0b10101, 0b10001, 0b10001, 0b10001},
	'n': {0b10001, 0b11001, 0b10101, 0b10011, 0b10001, 0b10001, 0b10001},
	'o': {0b01110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'p': {0b11110, 0b10001, 0b10001, 0b11110, 0b10000, 0b10000, 0b10000},
	'r': {0b11110, 0b10001, 0b10001, 0b11110, 0b10100, 0b10010, 0b10001},
	's': {0b01111, 0b10000, 0b10000, 0b01110, 0b00001, 0b00001, 0b11110},
	't': {0b11111, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100},
	'u': {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'v': {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01010, 0b00100},
	'w': {0b10001, 0b10001, 0b10001, 0b10101, 0b10101, 0b11011, 0b10001},
	'y': {0b10001, 0b10001, 0b01010, 0b00100, 0b00100, 0b00100, 0b00100},
	'.': {0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b00100},
	'!': {0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00000, 0b00100},
	' ': {0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b00000, 0b00000},
}

// ── Scrolling text message ──────────────────────────────────

const scrollMsg = "     welcome to codebase !     your local ai coding agent     built for the terminal     press enter to begin !     "

// ── Boot model ───────────────────────────────────────────────

// Demo phases (frame-based, at 20fps / 50ms per frame)
const (
	phPlasmaOnly   = 0   // frames 0-29:   just plasma + copper (1.5s)
	phLogoAppear   = 30  // frames 30-69:  logo assembles pixel by pixel (2s)
	phTextAppear   = 70  // frames 70-89:  "codebase" text types in (1s)
	phCubeAppear   = 90  // frames 90-109: cube fades in (1s)
	phScrollStart  = 60  // frames 60+:    scroller starts
	phStepsStart   = 80  // frames 80+:    boot steps begin appearing
)

type bootModel struct {
	config  *Config
	steps   []bootStep
	current int
	done    bool
	width   int
	height  int
	frame   int

	sin1 [512]float64
	sin2 [512]float64
	sin3 [512]float64
	sin4 [512]float64

	audio *AudioPlayer
}

func newBootModel(cfg *Config) bootModel {
	m := bootModel{
		config:  cfg,
		current: 0,
		steps:   buildBootSteps(cfg),
	}
	for i := range m.sin1 {
		x := float64(i) / 512.0 * math.Pi * 2
		m.sin1[i] = math.Sin(x)
		m.sin2[i] = math.Sin(x * 1.3)
		m.sin3[i] = math.Sin(x * 0.7)
		m.sin4[i] = math.Sin(x * 1.7)
	}
	return m
}

func buildBootSteps(cfg *Config) []bootStep {
	fileCount := 0
	filepath.Walk(cfg.WorkDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		fileCount++
		return nil
	})
	workDisplay := cfg.WorkDir
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(workDisplay, home) {
		workDisplay = "~" + workDisplay[len(home):]
	}
	return []bootStep{
		{label: "config", value: "loaded"},
		{label: "provider", value: cfg.Model},
		{label: "workspace", value: fmt.Sprintf("%s (%d files)", workDisplay, fileCount)},
		{label: "status", value: "ready"},
	}
}

// ── Bubble Tea ───────────────────────────────────────────────

func (m bootModel) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			// Start boot music in background (nil if no audio device)
			return bootAudioMsg{player: StartBootMusic()}
		},
		tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg { return demoTickMsg(t) }),
		// First boot step after 4 seconds (drawn-out demo intro)
		tea.Tick(4*time.Second, func(t time.Time) tea.Msg { return bootTickMsg{} }),
	)
}

func (m bootModel) Update(msg tea.Msg) (bootModel, tea.Cmd) {
	switch msg := msg.(type) {
	case bootAudioMsg:
		m.audio = msg.player

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case demoTickMsg:
		m.frame++
		return m, tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg { return demoTickMsg(t) })

	case bootTickMsg:
		if m.current < len(m.steps) {
			m.steps[m.current].done = true
			m.current++
		}
		if m.current >= len(m.steps) {
			m.done = true
			return m, tea.Tick(1200*time.Millisecond, func(t time.Time) tea.Msg {
				return bootDoneMsg{}
			})
		}
		return m, tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return bootTickMsg{} })

	case tea.KeyMsg:
		switch msg.String() {
		case "enter", " ":
			if m.done {
				return m, func() tea.Msg { return bootDoneMsg{} }
			}
			for i := range m.steps {
				m.steps[i].done = true
			}
			m.current = len(m.steps)
			m.done = true
			return m, tea.Tick(300*time.Millisecond, func(t time.Time) tea.Msg { return bootDoneMsg{} })
		}
	}
	return m, nil
}

// ── View — compositing pipeline ──────────────────────────────

func (m bootModel) View() string {
	if m.width < 10 || m.height < 5 {
		return ""
	}

	w := m.width
	h := m.height * 2
	px := make([]rgb, w*h)
	t := float64(m.frame) * 0.035

	// ── Always: Plasma background ────────────────────────────
	m.renderPlasma(px, w, h, t)
	m.renderCopperBars(px, w, h, t)
	m.renderScanlines(px, w, h)

	// ── Phase: Logo assembles ────────────────────────────────
	if m.frame >= phLogoAppear {
		logoScale := max(3, min(h/14, w/20))
		logoPxW := logoW * logoScale
		logoPxH := logoH * logoScale
		logoX := (w - logoPxW) / 2
		logoY := (h-logoPxH)/2 - int(float64(h)*0.18)

		// Reveal progress: 0.0 → 1.0 over the logo-appear phase
		reveal := math.Min(1.0, float64(m.frame-phLogoAppear)/40.0)
		m.renderLogo(px, w, h, logoX, logoY, logoScale, t, reveal)

		// ── Phase: "codebase" text types in ──────────────────
		if m.frame >= phTextAppear {
			textScale := max(1, logoScale/3)
			titleText := "codebase"
			charsVisible := min(len(titleText), (m.frame-phTextAppear)/3+1)
			visibleText := titleText[:charsVisible]

			glyphAdv := (5 + 2) * textScale
			fullW := len(titleText)*glyphAdv - 2*textScale
			textX := (w-fullW)/2 + 1
			textY := logoY + logoPxH + logoScale*3
			m.renderBitmapText(px, w, h, textX, textY, textScale, visibleText, t)
		}
	}

	// ── Phase: 3D cube spins in ──────────────────────────────
	if m.frame >= phCubeAppear {
		cubeSize := float64(min(w, h)) * 0.14
		cubeCX := float64(w) * 0.78
		cubeCY := float64(h) * 0.35
		// Grow from 0 to full size
		growT := math.Min(1.0, float64(m.frame-phCubeAppear)/30.0)
		m.renderCube(px, w, h, cubeCX, cubeCY, cubeSize*growT, t)
	}

	// ── Phase: Sine-wave scroller at the bottom ──────────────
	if m.frame >= phScrollStart {
		m.renderScroller(px, w, h, t)
	}

	// ── Phase: Boot steps ────────────────────────────────────
	if m.frame >= phStepsStart {
		m.renderBootInfo(px, w, h, t)
	}

	return m.encode(px, w, h)
}

// ── Plasma ───────────────────────────────────────────────────

func (m bootModel) renderPlasma(px []rgb, w, h int, t float64) {
	for py := 0; py < h; py++ {
		yf := float64(py) * 0.025
		for ppx := 0; ppx < w; ppx++ {
			xf := float64(ppx) * 0.02
			v1 := m.sinLookup(m.sin1[:], xf+t)
			v2 := m.sinLookup(m.sin2[:], yf+t*1.1)
			v3 := m.sinLookup(m.sin3[:], (xf+yf)*0.3+t*0.7)
			d := math.Sqrt(xf*xf+yf*yf) * 0.08
			v4 := m.sinLookup(m.sin4[:], d+t*0.5)
			v := (v1 + v2 + v3 + v4) / 4.0
			idx := int((v*0.5+0.5)*255) & 0xFF
			px[py*w+ppx] = plasmaPalette[idx]
		}
	}
}

// ── Copper bars ──────────────────────────────────────────────

func (m bootModel) renderCopperBars(px []rgb, w, h int, t float64) {
	barDefs := [3][5]float64{
		{0.6, 0.0, 1.4, 0.5, 0.15},
		{0.45, 2.2, 0.2, 0.6, 1.6},
		{0.8, 4.5, 1.2, 0.15, 1.0},
	}
	barWidth := math.Max(6, float64(h)*0.07)
	for _, bar := range barDefs {
		cy := math.Sin(t*bar[0]+bar[1])*float64(h)*0.35 + float64(h)*0.5
		for py := int(cy - barWidth*2); py <= int(cy+barWidth*2); py++ {
			if py < 0 || py >= h {
				continue
			}
			dist := math.Abs(float64(py) - cy)
			inten := math.Max(0, 1.0-dist/barWidth)
			inten = inten * inten * inten
			boost := inten * 140
			for ppx := 0; ppx < w; ppx++ {
				p := &px[py*w+ppx]
				p.r = clampU8(float64(p.r) + boost*bar[2])
				p.g = clampU8(float64(p.g) + boost*bar[3])
				p.b = clampU8(float64(p.b) + boost*bar[4])
			}
		}
	}
}

// ── Scanlines ────────────────────────────────────────────────

func (m bootModel) renderScanlines(px []rgb, w, h int) {
	for py := 0; py < h; py += 2 {
		for ppx := 0; ppx < w; ppx++ {
			p := &px[py*w+ppx]
			p.r = uint8(float64(p.r) * 0.82)
			p.g = uint8(float64(p.g) * 0.82)
			p.b = uint8(float64(p.b) * 0.82)
		}
	}
}

// ── 3D wireframe cube ────────────────────────────────────────

func (m bootModel) renderCube(px []rgb, w, h int, cx, cy, size, t float64) {
	if size < 2 {
		return
	}
	ay, ax, az := t*0.7, t*0.45, t*0.25
	cosY, sinY := math.Cos(ay), math.Sin(ay)
	cosX, sinX := math.Cos(ax), math.Sin(ax)
	cosZ, sinZ := math.Cos(az), math.Sin(az)

	var proj [8]vec2
	var depth [8]float64
	for i, v := range cubeVerts {
		rx := v.x*cosY - v.z*sinY
		rz := v.x*sinY + v.z*cosY
		ry := v.y
		ry2 := ry*cosX - rz*sinX
		rz2 := ry*sinX + rz*cosX
		rx2 := rx*cosZ - ry2*sinZ
		ry3 := rx*sinZ + ry2*cosZ
		fov := 3.5
		sc := fov / (rz2 + fov)
		proj[i] = vec2{rx2*sc*size + cx, ry3*sc*size + cy}
		depth[i] = rz2
	}

	for _, e := range cubeEdges {
		a, b := proj[e[0]], proj[e[1]]
		avgZ := (depth[e[0]] + depth[e[1]]) / 2.0
		bright := math.Max(0.35, math.Min(1.0, 0.6+avgZ*0.2))
		col := rgb{clampU8(bright * 100), clampU8(bright * 235), clampU8(bright * 255)}
		drawLine(px, w, h, int(a.x), int(a.y), int(b.x), int(b.y), col)
	}

	for _, p := range proj {
		ix, iy := int(p.x), int(p.y)
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				x, y := ix+dx, iy+dy
				if x >= 0 && x < w && y >= 0 && y < h {
					px[y*w+x] = rgb{200, 250, 255}
				}
			}
		}
	}
}

func drawLine(px []rgb, w, h, x0, y0, x1, y1 int, col rgb) {
	dx := iabs(x1 - x0)
	dy := -iabs(y1 - y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		if x0 >= 0 && x0 < w && y0 >= 0 && y0 < h {
			p := &px[y0*w+x0]
			p.r = clampU8(float64(p.r)*0.3 + float64(col.r))
			p.g = clampU8(float64(p.g)*0.3 + float64(col.g))
			p.b = clampU8(float64(p.b)*0.3 + float64(col.b))
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

// ── Pixel "C" logo with progressive reveal ──────────────────

func (m bootModel) renderLogo(px []rgb, w, h, ox, oy, scale int, t, reveal float64) {
	totalPixels := 0
	for ly := 0; ly < logoH; ly++ {
		for lx := 0; lx < logoW; lx++ {
			if logoC[ly][lx] {
				totalPixels++
			}
		}
	}
	pixelsToShow := int(reveal * float64(totalPixels))

	glowR := scale/2 + 2
	shown := 0
	for ly := 0; ly < logoH; ly++ {
		for lx := 0; lx < logoW; lx++ {
			if !logoC[ly][lx] {
				// Darken background behind the logo for contrast
				if reveal > 0.3 {
					darkFade := math.Min(1.0, (reveal-0.3)/0.3)
					dim := 1.0 - darkFade*0.7
					for dy := 0; dy < scale; dy++ {
						for dx := 0; dx < scale; dx++ {
							x := ox + lx*scale + dx
							y := oy + ly*scale + dy
							if x >= 0 && x < w && y >= 0 && y < h {
								p := &px[y*w+x]
								p.r = uint8(float64(p.r) * dim)
								p.g = uint8(float64(p.g) * dim)
								p.b = uint8(float64(p.b) * dim)
							}
						}
					}
				}
				continue
			}

			shown++
			if shown > pixelsToShow {
				continue
			}

			hue := float64(ly+lx)/float64(logoW+logoH) + t*0.35
			hue -= math.Floor(hue)
			cr, cg, cb := hslToRGB(hue, 0.9, 0.75)

			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					x := ox + lx*scale + dx
					y := oy + ly*scale + dy
					if x >= 0 && x < w && y >= 0 && y < h {
						px[y*w+x] = rgb{cr, cg, cb}
					}
				}
			}

			// Glow
			for dy := -glowR; dy <= scale+glowR; dy++ {
				for dx := -glowR; dx <= scale+glowR; dx++ {
					x := ox + lx*scale + dx
					y := oy + ly*scale + dy
					if x < 0 || x >= w || y < 0 || y >= h {
						continue
					}
					if dx >= 0 && dx < scale && dy >= 0 && dy < scale {
						continue
					}
					cdx := math.Max(0, math.Max(float64(-dx), float64(dx-scale+1)))
					cdy := math.Max(0, math.Max(float64(-dy), float64(dy-scale+1)))
					dist := math.Sqrt(cdx*cdx + cdy*cdy)
					if dist > float64(glowR) {
						continue
					}
					inten := (1.0 - dist/float64(glowR)) * 0.4
					p := &px[y*w+x]
					p.r = clampU8(float64(p.r) + float64(cr)*inten)
					p.g = clampU8(float64(p.g) + float64(cg)*inten)
					p.b = clampU8(float64(p.b) + float64(cb)*inten)
				}
			}
		}
	}
}

// ── Bitmap text ──────────────────────────────────────────────

func (m bootModel) renderBitmapText(px []rgb, w, h, ox, oy, scale int, text string, t float64) {
	glyphAdv := (5 + 2) * scale
	for ci, ch := range text {
		glyph, ok := glyphMap[ch]
		if !ok {
			continue
		}
		gx := ox + ci*glyphAdv
		for row := 0; row < 7; row++ {
			bits := glyph[row]
			for col := 0; col < 5; col++ {
				if bits&(1<<(4-col)) == 0 {
					continue
				}
				hue := float64(ci)/float64(max(1, len(text))) + t*0.25
				hue -= math.Floor(hue)
				cr, cg, cb := hslToRGB(hue, 0.65, 0.82)
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						x := gx + col*scale + dx
						y := oy + row*scale + dy
						if x >= 0 && x < w && y >= 0 && y < h {
							px[y*w+x] = rgb{cr, cg, cb}
						}
					}
				}
			}
		}
	}
}

// ── Sine-wave scrolling text (classic Amiga scroller) ────────

func (m bootModel) renderScroller(px []rgb, w, h int, t float64) {
	scrollScale := max(1, min(h/30, w/60))
	scrollH := 7 * scrollScale
	baseY := h - scrollH - 4 // near bottom

	// Horizontal scroll position (pixels)
	glyphAdv := (5 + 2) * scrollScale
	totalScrollW := len(scrollMsg) * glyphAdv
	scrollOffset := (m.frame * 2) % totalScrollW

	// Darken a band behind the scroller with gradient fade
	fadeTop := baseY - scrollScale*4
	fadeBot := baseY + scrollH + scrollScale*2
	for py := fadeTop; py < fadeBot; py++ {
		if py < 0 || py >= h {
			continue
		}
		// Gradient: full darkness at center, fading at edges
		centerY := float64(baseY + scrollH/2)
		halfRange := float64(fadeBot-fadeTop) / 2.0
		distFromCenter := math.Abs(float64(py) - centerY)
		fade := math.Max(0, 1.0-distFromCenter/halfRange)
		dim := 1.0 - fade*0.7
		for ppx := 0; ppx < w; ppx++ {
			p := &px[py*w+ppx]
			p.r = uint8(float64(p.r) * dim)
			p.g = uint8(float64(p.g) * dim)
			p.b = uint8(float64(p.b) * dim)
		}
	}

	// Rainbow separator line above scroller
	sepY := fadeTop
	if sepY >= 0 && sepY < h {
		for ppx := 0; ppx < w; ppx++ {
			hue := float64(ppx)/float64(w) + t*0.3
			hue -= math.Floor(hue)
			cr, cg, cb := hslToRGB(hue, 0.9, 0.55)
			px[sepY*w+ppx] = rgb{cr, cg, cb}
		}
	}

	// Render each visible character with sine-wave y displacement
	for ci := 0; ci < len(scrollMsg); ci++ {
		ch := rune(scrollMsg[ci])
		glyph, ok := glyphMap[ch]
		if !ok {
			continue
		}

		charX := ci*glyphAdv - scrollOffset
		// Wrap around
		if charX < -glyphAdv {
			charX += totalScrollW
		}
		if charX > w+glyphAdv {
			continue
		}

		// Sine-wave vertical offset per character
		sineAngle := float64(ci)*0.4 + t*3.0
		sineOffset := math.Sin(sineAngle) * float64(scrollScale*3)
		charY := baseY + int(sineOffset)

		// Color: rainbow shift along scroll
		hue := float64(ci)/float64(len(scrollMsg)) + t*0.15
		hue -= math.Floor(hue)
		cr, cg, cb := hslToRGB(hue, 0.95, 0.7)

		for row := 0; row < 7; row++ {
			bits := glyph[row]
			for col := 0; col < 5; col++ {
				if bits&(1<<(4-col)) == 0 {
					continue
				}
				for dy := 0; dy < scrollScale; dy++ {
					for dx := 0; dx < scrollScale; dx++ {
						x := charX + col*scrollScale + dx
						y := charY + row*scrollScale + dy
						if x >= 0 && x < w && y >= 0 && y < h {
							px[y*w+x] = rgb{cr, cg, cb}
						}
					}
				}
			}
		}
	}
}

// ── Boot info (overlaid on scroller area) ────────────────────

func (m bootModel) renderBootInfo(px []rgb, w, h int, t float64) {
	lines := m.buildStepStrings()
	if len(lines) == 0 {
		return
	}

	// Position steps just above the scroller band
	scrollScale := max(1, min(h/30, w/60))
	scrollH := 7 * scrollScale
	scrollBaseY := h - scrollH - 4

	stepLineH := 3
	totalStepH := len(lines) * stepLineH
	startY := scrollBaseY - scrollScale*5 - totalStepH

	// Gentle gradient darken behind steps
	gradTop := startY - 2
	gradBot := startY + totalStepH + 2
	for py := gradTop; py < gradBot; py++ {
		if py < 0 || py >= h {
			continue
		}
		centerY := float64(startY + totalStepH/2)
		halfR := float64(gradBot-gradTop) / 2.0
		distC := math.Abs(float64(py) - centerY)
		fade := math.Max(0, 1.0-distC/halfR)
		dim := 1.0 - fade*0.55
		for ppx := 0; ppx < w; ppx++ {
			p := &px[py*w+ppx]
			p.r = uint8(float64(p.r) * dim)
			p.g = uint8(float64(p.g) * dim)
			p.b = uint8(float64(p.b) * dim)
		}
	}

	// Render step text
	for li, line := range lines {
		baseY := startY + li*stepLineH
		baseX := (w - len(line)) / 2
		if baseX < 2 {
			baseX = 2
		}
		for ci, ch := range line {
			if ch == ' ' {
				continue
			}
			x := baseX + ci
			if x < 0 || x >= w {
				continue
			}
			var cr, cg, cb uint8
			if ch == '.' || ch == '/' || ch == '-' || ch == '\\' || ch == '|' {
				cr, cg, cb = 80, 95, 110
			} else {
				cr, cg, cb = 210, 220, 235
			}
			for dy := 0; dy < 2; dy++ {
				y := baseY + dy
				if y >= 0 && y < h {
					px[y*w+x] = rgb{cr, cg, cb}
				}
			}
		}
	}
}

func (m bootModel) buildStepStrings() []string {
	var lines []string
	spinChars := []string{"/", "-", "\\", "|"}
	for i, step := range m.steps {
		dots := strings.Repeat(".", 18-len(step.label))
		if step.done {
			lines = append(lines, fmt.Sprintf(" %s %s %s", step.label, dots, step.value))
		} else if i == m.current {
			lines = append(lines, fmt.Sprintf(" %s %s %s", step.label, dots, spinChars[m.frame/2%len(spinChars)]))
		} else {
			lines = append(lines, fmt.Sprintf(" %s %s ...", step.label, dots))
		}
	}
	if m.done {
		lines = append(lines, "")
		if (m.frame/12)%2 == 0 {
			lines = append(lines, " >> press enter to begin <<")
		} else {
			lines = append(lines, "")
		}
	}
	return lines
}

// ── Half-block encoder ───────────────────────────────────────

func (m bootModel) encode(px []rgb, w, h int) string {
	var sb strings.Builder
	sb.Grow(w * m.height * 25)
	for row := 0; row < m.height; row++ {
		topY := row * 2
		botY := row*2 + 1
		if botY >= h {
			botY = topY
		}
		var prevFg, prevBg rgb
		first := true
		for col := 0; col < w; col++ {
			fg := px[topY*w+col].quantize()
			bg := px[botY*w+col].quantize()
			fgCh := first || fg != prevFg
			bgCh := first || bg != prevBg
			if fgCh && bgCh {
				fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm", fg.r, fg.g, fg.b, bg.r, bg.g, bg.b)
			} else if fgCh {
				fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%dm", fg.r, fg.g, fg.b)
			} else if bgCh {
				fmt.Fprintf(&sb, "\x1b[48;2;%d;%d;%dm", bg.r, bg.g, bg.b)
			}
			sb.WriteRune('▀')
			prevFg, prevBg = fg, bg
			first = false
		}
		sb.WriteString("\x1b[0m")
		if row < m.height-1 {
			sb.WriteRune('\n')
		}
	}
	return sb.String()
}

// ── Helpers ──────────────────────────────────────────────────

func (m *bootModel) sinLookup(table []float64, x float64) float64 {
	idx := int(x*512.0/(2.0*math.Pi)) % 512
	if idx < 0 {
		idx += 512
	}
	return table[idx]
}

func clampU8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v)
}

func hslToRGB(h, s, l float64) (uint8, uint8, uint8) {
	if s == 0 {
		v := uint8(l * 255)
		return v, v, v
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	return uint8(hueToRGB(p, q, h+1.0/3.0) * 255),
		uint8(hueToRGB(p, q, h) * 255),
		uint8(hueToRGB(p, q, h-1.0/3.0) * 255)
}

func hueToRGB(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6.0:
		return p + (q-p)*6*t
	case t < 0.5:
		return q
	case t < 2.0/3.0:
		return p + (q-p)*(2.0/3.0-t)*6
	default:
		return p
	}
}

func iabs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
