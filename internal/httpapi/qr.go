package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"rsc.io/qr"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/localnet"
)

// The QR code, per FR-002.
//
// It is rendered as SVG rather than a raster image: it scales to whatever the
// desktop window is, costs a couple of kilobytes, and needs no image encoder.
// The phone's camera reads it the same either way.
//
// Encoding is done by rsc.io/qr rather than by hand. QR is a specified format
// with masking, error correction, and version selection, and a subtly wrong
// implementation produces a code that scans on the phone you tested with and
// fails on the next one.

// qrQuietZone is the mandatory white border, in modules. The specification
// requires four; without it, many scanners simply do not see the code.
const qrQuietZone = 4

// handleQR renders the connection URL as a scannable code.
//
// Unauthenticated, like /connect: the URL it encodes is the address of a server
// already advertising itself over mDNS, and the code alone grants nothing. What
// grants access is the pairing code, which is never in here.
func (d Deps) handleQR(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	if target == "" {
		d.writeError(w, r, app.New(app.CodeInvalidRequest))
		return
	}
	// Only a local address may be encoded. Rendering an arbitrary URL would
	// turn this endpoint into a way to put someone else's link on the user's
	// screen looking as if fastr had produced it.
	if !isLocalURL(target) {
		d.writeError(w, r, app.New(app.CodeInvalidRequest))
		return
	}

	// L is the lowest error correction level and therefore the densest code.
	// A screen is not a printed label: it is not going to be smudged, and the
	// fewer modules there are the easier it is to scan from across a room.
	code, err := qr.Encode(target, qr.L)
	if err != nil {
		d.writeError(w, r, app.Errorf(app.CodeInternal, err))
		return
	}

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	securityHeaders(w)

	if _, err := w.Write([]byte(renderSVG(code))); err != nil {
		d.Log.Debug("qr write failed", "error", err)
	}
}

// renderSVG turns the module matrix into a scalable image.
//
// Modules are emitted as one path of rectangles rather than one element each,
// which keeps a typical code around two kilobytes instead of twenty.
func renderSVG(code *qr.Code) string {
	size := code.Size
	total := size + qrQuietZone*2

	var path strings.Builder
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&path, "M%d %dh1v1h-1z", x+qrQuietZone, y+qrQuietZone)
			}
		}
	}

	var out strings.Builder
	fmt.Fprintf(&out,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" `+
			`shape-rendering="crispEdges" role="img" aria-label="Pairing QR code">`,
		total, total)
	// An explicit white background: a transparent code on a dark theme is
	// invisible to a scanner, which expects dark modules on light.
	fmt.Fprintf(&out, `<rect width="%d" height="%d" fill="#ffffff"/>`, total, total)
	fmt.Fprintf(&out, `<path fill="#000000" d="%s"/>`, path.String())
	out.WriteString(`</svg>`)

	return out.String()
}

// isLocalURL reports whether a URL points at the local network.
func isLocalURL(raw string) bool {
	rest, ok := strings.CutPrefix(raw, "http://")
	if !ok {
		rest, ok = strings.CutPrefix(raw, "https://")
		if !ok {
			return false
		}
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return rest != "" && localnet.IsAddr(rest)
}
