// Package guidance provides language-specific prompt guidance injected into verb
// system prompts when a project's language can be detected from the workspace.
package guidance

import (
	"os"
	"path/filepath"
)

// Inject appends language-specific guidance to a system prompt and returns the
// result. If no language can be detected from folderPath, the prompt is returned
// unchanged.
func Inject(systemPrompt, folderPath string) string {
	g := load(folderPath)
	if g == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n## Language-Specific Guidance\n\n" + g
}

func load(folderPath string) string {
	switch detect(folderPath) {
	case "go":
		return goGuidance
	default:
		return ""
	}
}

func detect(folderPath string) string {
	if exists(filepath.Join(folderPath, "go.mod")) {
		return "go"
	}
	if exists(filepath.Join(folderPath, "requirements.txt")) ||
		exists(filepath.Join(folderPath, "setup.py")) ||
		exists(filepath.Join(folderPath, "pyproject.toml")) {
		return "python"
	}
	if exists(filepath.Join(folderPath, "composer.json")) {
		return "php"
	}
	if exists(filepath.Join(folderPath, "package.json")) {
		return "javascript"
	}
	if exists(filepath.Join(folderPath, "Cargo.toml")) {
		return "rust"
	}
	return ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

const goGuidance = `You are working on a Go project. Apply these language-specific rules:

**Pixel and color access (image package):**
` + "`img.At(x, y).RGBA()`" + ` returns a 4-value tuple ` + "`(r, g, b, a uint32)`" + ` — NOT a struct.
Always destructure it:
  r32, g32, b32, _ := img.At(x, y).RGBA()   // values are in range [0, 65535]
  r8 := uint8(r32 >> 8)                       // convert to uint8

For ` + "`*image.NRGBA`" + `, direct Pix access is simpler and avoids the tuple entirely:
  offset := (y-bounds.Min.Y)*img.Stride + (x-bounds.Min.X)*4
  r := img.Pix[offset+0]   // uint8, R channel
  g := img.Pix[offset+1]   // uint8, G channel
  b := img.Pix[offset+2]   // uint8, B channel
  // img.Pix[offset+3] is alpha — read it but do not modify it for steganography

**Variable scope in if/else:**
Variables declared inside a branch are NOT visible outside it. Declare before the block:
  // WRONG:
  if ok { img = nrgba } else { bounds := img.Bounds(); img = image.NewNRGBA(bounds) }
  img.Pix[bounds.Min.Y...]   // ERROR: bounds undefined here

  // CORRECT:
  bounds := img.Bounds()
  if ok { img = nrgba } else { img = image.NewNRGBA(bounds) }
  img.Pix[bounds.Min.Y...]   // bounds is in scope

**Big-endian integer encoding:**
  import "encoding/binary"
  var buf [4]byte
  binary.BigEndian.PutUint32(buf[:], val)   // write uint32
  val := binary.BigEndian.Uint32(buf[:])    // read uint32

**Compile-time interface assertions (preferred over runtime type checks):**
  var _ image.Image = (*image.NRGBA)(nil)   // fails at build time if signature wrong

**LSB bit manipulation:**
  Set LSB:            b = (b & 0xFE) | (bit & 0x01)    // bit must be 0 or 1
  Extract LSB:        bit := b & 0x01
  Extract bit N:      bit := (b >> n) & 0x01            // n=0 is LSB, n=7 is MSB
  Embed bit MSB-first into successive channel LSBs:
    for i := 7; i >= 0; i-- {
        bit := (msgByte >> i) & 0x01
        channelByte = (channelByte & 0xFE) | bit
    }

**Build and test commands:**
  go build ./...                     // build all packages
  go test -v -run TestName ./...     // run a specific test
  go test -v ./...                   // run all tests
  go mod init <modulename>           // initialize module (only if go.mod absent)
  go mod tidy                        // sync go.sum after adding imports`
