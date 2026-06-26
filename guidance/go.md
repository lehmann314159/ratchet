You are working on a Go project. Apply these language-specific rules:

**Orient step (step 1 in your process):**
  Source files to read: all *.go files in the project root (not in subdirectories unless
  the spec places code there).
  Build command: go build ./...
  Stale file cleanup (step 2): overwrite a stray .go file with only its package line,
  e.g.: package stego

**Pixel and color access (image package):**
`img.At(x, y).RGBA()` returns a 4-value tuple `(r, g, b, a uint32)` — NOT a struct.
Always destructure it:
  r32, g32, b32, _ := img.At(x, y).RGBA()   // values are in range [0, 65535]
  r8 := uint8(r32 >> 8)                       // convert to uint8

For `*image.NRGBA`, direct Pix access is simpler and avoids the tuple entirely:
  offset := (y-bounds.Min.Y)*img.Stride + (x-bounds.Min.X)*4
  r := img.Pix[offset+0]   // uint8, R channel
  g := img.Pix[offset+1]   // uint8, G channel
  b := img.Pix[offset+2]   // uint8, B channel
  // img.Pix[offset+3] is alpha — read it but do not modify it unless your spec requires it

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

**Compile-time assertions (preferred over runtime type checks):**
Use these in api_check_test.go or anywhere you need to lock a signature at build time:

  // Function signature check — fails at build if the function signature is wrong:
  var _ func(image.Image, string) (image.Image, error) = Encode
  var _ func(image.Image) (string, error) = Decode

  // Interface implementation check — fails at build if type doesn't implement interface:
  var _ image.Image = (*image.NRGBA)(nil)

Never use runtime type assertions (var f interface{} = Fn; _, ok := f.(func...)) for this
purpose — they only fail when the test runs, not at build time.

**LSB bit manipulation:**
  Set LSB:            b = (b & 0xFE) | (bit & 0x01)    // bit must be 0 or 1
  Extract LSB:        bit := b & 0x01
  Extract bit N:      bit := (b >> n) & 0x01            // n=0 is LSB, n=7 is MSB
  Embed bit MSB-first into successive channel LSBs:
    for i := 7; i >= 0; i-- {
        bit := (msgByte >> i) & 0x01
        channelByte = (channelByte & 0xFE) | bit
    }

**Imports:**
Only import packages you are actually using in the current file. Go will refuse to compile
if any import is unused. Add imports as you write code that needs them, not speculatively.

**Build and test commands:**
  go build ./...                     // build all packages
  go test -v -run TestName ./...     // run a specific test
  go test -v ./...                   // run all tests
  go mod init <modulename>           // initialize module (only if go.mod absent)
  go mod tidy                        // sync go.sum after adding imports
