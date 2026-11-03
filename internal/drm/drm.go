// Package drm implements Microsoft Edge's Sec-MS-GEC token generation.
//
// The token is an anti-abuse credential required by the Read Aloud WebSocket
// endpoint. Microsoft rejects the historical plain-SHA256 scheme; the current
// v2 scheme emits "<cs64>-<sha256>" where both halves derive from a
// deliberately obfuscated compression function rather than standard SHA-256.
//
// This is a faithful port of the reference implementation, verified against
// test vectors captured from it. Do not "simplify" the constants: several of
// them deviate from FIPS 180-4 on purpose and the service checks the result.
package drm

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"strconv"
)

// TrustedClientToken is the public client token embedded in the Edge browser.
const TrustedClientToken = "6A5AA1D4EAFF4E9FB37E23D68491D6F4"

// winEpoch is the number of seconds between the Unix epoch and the Windows
// file time epoch (1601-01-01).
const winEpoch = 11644473600

// tokenWindowSeconds is the granularity the service rounds timestamps to.
const tokenWindowSeconds = 300

var sha256Initial = [8]uint32{
	0x6A09E667, 0xBB67AE85, 0x3C6EF372, 0xA54FF53A,
	0x510E527F, 0x9B05688C, 0x1F83D9AB, 0x5BE0CD19,
}

var sha256State = [8]uint32{
	0xFD9FBEA2, 0x91243CE7, 0xBE499094, 0xCD2E1E0A,
	0x6C006F49, 0x04BD35AB, 0xFEB0A7E4, 0x03EFFBD2,
}

var sha256Schedule = [14]uint32{
	0xEB24BF9E, 0x1558691F, 0xA0C09EE2, 0x0CFA32BA,
	0xA622EE8C, 0xD1DD9491, 0xC0DD25A0, 0xF9E70AB6,
	0xF0C31573, 0x7058CE4E, 0x7188E606, 0x00E5AB33,
	0xD258B060, 0x72766564,
}

var sha256RoundConstants = [64]uint32{
	0x428A2F98, 0x71374491, 0xB5C0FBCF, 0xE9B5DBA5,
	0x3956C25B, 0x59F111F1, 0x923F82A4, 0xAB1C5ED5,
	0xD807AA98, 0x12835B01, 0x243185BE, 0x550C7DC3,
	0x72BE5D74, 0x80DEB1FE, 0x9BDC06A7, 0xC19BF174,
	0xE49B69C1, 0xEFBE4786, 0x0FC19DC6, 0x240CA1CC,
	0x2DE92C6F, 0x4A7484AA, 0x5CB0A9DC, 0x76F988DA,
	0x983E5152, 0xA831C66D, 0xB00327C8, 0xBF597FC7,
	0xC6E00BF3, 0xD5A79147, 0x06CA6351, 0x14292967,
	0x27B70A85, 0x2E1B2138, 0x4D2C6DFC, 0x53380D13,
	0x650A7354, 0x766A0ABB, 0x81C2C92E, 0x92722C85,
	0xA2BFE8A1, 0xA81A664B, 0xC24B8B70, 0xC76C51A3,
	0xD192E819, 0xD6990624, 0xF40E3585, 0x106AA070,
	0x19A4C116, 0x1E376C08, 0x2748774C, 0x34B0BCB5,
	0x391C0CB3, 0x4ED8AA4A, 0x5B9CCA4F, 0x682E6FF3,
	0x748F82EE, 0x78A5636F, 0x84C87814, 0x8CC70208,
	0x90BEFFFA, 0xA4506CEB, 0xBEF9A3F7, 0xC67178F2,
}

// sha256Adjustment replaces round constant 14 in the first-block schedule.
const sha256Adjustment = 0xC9DC06A7

var sha256BlockConstants = func() [64]uint32 {
	var c [64]uint32
	copy(c[:], sha256RoundConstants[:])
	c[14] = sha256Adjustment
	return c
}()

const (
	sha256Sigma0Adjustment = 0x059C0B80
	sha256Sigma1Adjustment = 0x000B92C0
)

type cs64Coefficient struct {
	coefficient uint32
	offset      uint32
}

var cs64Coefficients = [14]cs64Coefficient{
	{0xE82E91B1, 0xB32700D0},
	{0x44DE24C6, 0x88DFF51D},
	{0x3BEB65BC, 0x29514038},
	{0xC89C5933, 0xB328CBDF},
	{0x8D1EC211, 0xD405B4D5},
	{0x314EF5D5, 0x26E55F3C},
	{0xE92491B1, 0x607801C6},
	{0xF0A5B9D5, 0x82B2C1FB},
	{0x06064CF3, 0xFA51A3DA},
	{0xDA79FA60, 0x207CC464},
	{0xBED20560, 0x3C3C74F3},
	{0x89F9C9F8, 0xD2D14B61},
	{0x35FA38C6, 0xBB04A2C1},
	{0xCB49E3C6, 0xC5626167},
}

const (
	cs64Factor      = 0x82893933
	cs64Adjustment0 = 0xF2F90B1A
	cs64Adjustment1 = 0x0B1A0000
)

func rol16(v uint32) uint32 {
	return bits.RotateLeft32(v, 16)
}

// sigma0 and sigma1 are the SHA-256 small-sigma functions expressed as plain
// shift/XOR chains, matching the reference implementation bit for bit.
func sigma0(v uint32) uint32 {
	return (v >> 7) ^ (v << 25) ^ (v >> 18) ^ (v << 14) ^ (v >> 3)
}

func sigma1(v uint32) uint32 {
	return (v >> 17) ^ (v << 15) ^ (v >> 19) ^ (v << 13) ^ (v >> 10)
}

func sha256Rounds(state [8]uint32, words []uint32, firstRound int) [8]uint32 {
	constants := &sha256RoundConstants
	if firstRound == 14 {
		constants = &sha256BlockConstants
	}
	a, b, c, d, e, f, g, h := state[0], state[1], state[2], state[3], state[4], state[5], state[6], state[7]
	for i := firstRound; i < 64; i++ {
		s1 := bits.RotateLeft32(e, -6) ^ bits.RotateLeft32(e, -11) ^ bits.RotateLeft32(e, -25)
		ch := g ^ (e & (f ^ g))
		t1 := h + s1 + ch + constants[i] + words[i]
		s0 := bits.RotateLeft32(a, -2) ^ bits.RotateLeft32(a, -13) ^ bits.RotateLeft32(a, -22)
		maj := (a & b) | (c & (a | b))
		t2 := s0 + maj
		h = g
		g = f
		f = e
		e = d + t1
		d = c
		c = b
		b = a
		a = t1 + t2
	}
	return [8]uint32{a, b, c, d, e, f, g, h}
}

// expandSchedule fills words[first:] using the SHA-256 message schedule.
func expandSchedule(words []uint32, first int) []uint32 {
	for i := first; i < 64; i++ {
		x := words[i-2]
		y := words[i-15]
		s1 := (x >> 17) ^ (x << 15) ^ (x >> 19) ^ (x << 13) ^ (x >> 10)
		s0 := (y >> 7) ^ (y << 25) ^ (y >> 18) ^ (y << 14) ^ (y >> 3)
		words = append(words, s1+words[i-7]+s0+words[i-16])
	}
	return words
}

// sha256Init builds the first 64-word block from the 7-byte salt prefix.
func sha256Init(data []byte) [8]uint32 {
	words := make([]uint32, 0, 64)
	words = append(words, make([]uint32, 14)...)
	words = append(words,
		uint32(data[0])<<16|uint32(data[1])<<8|uint32(data[2]),
		binary.BigEndian.Uint32(data[3:7]),
	)
	for i := 16; i < 30; i++ {
		value := sha256Schedule[i-16]
		if i-2 >= 14 {
			if i == 16 {
				value += sigma1(words[i-2]) ^ sha256Sigma1Adjustment
			} else {
				value += sigma1(words[i-2])
			}
		}
		if i-7 >= 14 {
			value += words[i-7]
		}
		if i-15 >= 14 {
			if i == 29 {
				value += sigma0(words[i-15]) ^ sha256Sigma0Adjustment
			} else {
				value += sigma0(words[i-15])
			}
		}
		words = append(words, value)
	}
	words = append(words,
		sigma1(words[28])+words[23]+sigma0(words[15])+words[14]+
			sha256Adjustment-sha256RoundConstants[14],
	)
	words = expandSchedule(words, 31)
	working := sha256Rounds(sha256State, words, 14)
	var out [8]uint32
	for i := range out {
		out[i] = sha256Initial[i] + working[i]
	}
	return out
}

// sha256Digest is the obfuscated digest: a standard-shaped compression over a
// fixed leading block, with a modified initial state and round constant.
func sha256Digest(data []byte) []byte {
	if len(data) < 7 {
		panic("drm: token salt must contain at least seven bytes")
	}
	length := 57 + len(data)
	padLen := (55 - length) % 64
	if padLen < 0 {
		padLen += 64
	}
	padded := make([]byte, 0, len(data)-7+1+padLen+8)
	padded = append(padded, data[7:]...)
	padded = append(padded, 0x80)
	padded = append(padded, make([]byte, padLen)...)
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], uint64(length)*8)
	padded = append(padded, lenBuf[:]...)

	state := sha256Init(data[:7])
	for offset := 0; offset < len(padded); offset += 64 {
		words := make([]uint32, 0, 64)
		for i := 0; i < 16; i++ {
			words = append(words, binary.BigEndian.Uint32(padded[offset+i*4:]))
		}
		words = expandSchedule(words, 16)
		working := sha256Rounds(state, words, 0)
		for i := range state {
			state[i] += working[i]
		}
	}
	out := make([]byte, 32)
	for i, v := range state {
		binary.BigEndian.PutUint32(out[i*4:], v)
	}
	return out
}

// wordsLE4Even splits data into little-endian 32-bit words, zero-padded to an
// even word count.
func wordsLE4Even(data []byte) []uint32 {
	padded := make([]byte, len(data))
	copy(padded, data)
	if rem := len(padded) % 4; rem != 0 {
		padded = append(padded, make([]byte, 4-rem)...)
	}
	if len(padded) == 0 {
		return nil
	}
	words := make([]uint32, 0, len(padded)/4+1)
	for i := 0; i < len(padded); i += 4 {
		words = append(words, binary.LittleEndian.Uint32(padded[i:]))
	}
	if len(words)%2 != 0 {
		words = append(words, 0)
	}
	return words
}

func cs64Digest(data, digest []byte) (uint32, uint32) {
	keyA := binary.LittleEndian.Uint32(digest[0:4]) | 1
	keyB := binary.LittleEndian.Uint32(digest[4:8]) | 1
	factors := [2]uint32{keyA * cs64Factor, keyB * cs64Factor}

	terms := make([]uint32, 0, 14+len(data)+4)
	offsets := make([]uint32, 0, 14+len(data)+4)
	for i, c := range cs64Coefficients {
		terms = append(terms, c.coefficient*factors[i%2])
		offsets = append(offsets, c.offset)
	}

	body := make([]byte, 0, len(data)+1)
	body = append(body, 0)
	body = append(body, data...)
	for i, word := range wordsLE4Even(body) {
		key := keyA
		offsetFactor := uint32(0xEF0569FB)
		if i%2 != 0 {
			key = keyB
			offsetFactor = 0xC31713DB
		}
		term := word * key
		offset := rol16(word) * offsetFactor
		if i == 0 {
			term += cs64Adjustment0 * factors[0]
			offset += cs64Adjustment1
		}
		terms = append(terms, term)
		offsets = append(offsets, offset)
	}

	var swapAcc, swapLast, state, acc uint32
	for i := 0; i < len(terms); i += 2 {
		x, y := terms[i], terms[i+1]
		t := rol16(offsets[i] + x)
		q := t * 0x76F50F34
		swapAcc += rol16(q)*0xC3EFEA97 + q
		t = rol16(offsets[i+1] + y)
		q = t * 0x1881CE3C
		swapLast = rol16(q)*0x35BD1EC9 + q
		swapAcc += swapLast

		state = state*keyA + x
		state = rol16(state) * 0xCF98B111
		state = rol16(state) * 0x87085B9F
		state = rol16(state) * 0x12CEB96D
		state = rol16(state) * 0x257E1D83
		acc += state
		t = state*keyB + y
		t = rol16(t) * 0xA27416F5
		t = rol16(t) * 0xD38396FF
		state = rol16(rol16(t)*0x7C932B89) * 0xBFA49F69
		acc += state
	}
	return swapLast ^ state, swapAcc ^ acc
}

// TokenHash returns the CS64 and SHA-256 halves for raw token input.
func TokenHash(data []byte) (cs64, sha string) {
	digest := sha256Digest(data)
	a, b := cs64Digest(data, digest)
	cs := make([]byte, 8)
	binary.LittleEndian.PutUint32(cs[0:4], a)
	binary.LittleEndian.PutUint32(cs[4:8], b)
	return fmt.Sprintf("%X", cs), fmt.Sprintf("%X", digest)
}

// Generate returns the Sec-MS-GEC token for the given Unix timestamp. A
// negative timestamp means "use the current time".
func Generate(unixTimestamp float64) string {
	if unixTimestamp < 0 {
		unixTimestamp = nowUnix()
	}
	ticks := unixTimestamp + winEpoch
	ticks -= math.Mod(ticks, tokenWindowSeconds)
	ticks *= 1e7
	strToHash := strconv.FormatFloat(ticks, 'f', 0, 64) + TrustedClientToken
	cs64, sha := TokenHash([]byte(strToHash))
	return cs64 + "-" + sha
}

// GenerateNow is Generate with the current wall clock.
func GenerateNow() string {
	return Generate(-1)
}
