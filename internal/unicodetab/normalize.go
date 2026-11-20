package unicodetab

// NFC and NFD as CPython's unicodedata.normalize computes them, from the oracle's decompositions,
// combining classes and composing pairs.

const (
	hangulS      = 0xAC00
	hangulL      = 0x1100
	hangulV      = 0x1161
	hangulT      = 0x11A7
	hangulLCount = 19
	hangulVCount = 21
	hangulTCount = 28
	hangulN      = hangulVCount * hangulTCount
	hangulSCount = hangulLCount * hangulN
)

var (
	decompositions = mappings(decompositionEntries)
	compositions   = map[[2]rune]rune{}
	composeFirst   = map[rune]bool{}
)

func init() {
	for _, e := range compositionEntries {
		compositions[[2]rune{e[0], e[1]}] = e[2]
		composeFirst[e[0]] = true
	}
}

// NFD decomposes each code point fully and puts each run of combining marks in canonical order.
func NFD(s []rune) []rune {
	out := make([]rune, 0, len(s))
	for _, c := range s {
		if c >= hangulS && c < hangulS+hangulSCount {
			index := c - hangulS
			out = append(out, hangulL+index/hangulN, hangulV+(index%hangulN)/hangulTCount)
			if t := index % hangulTCount; t != 0 {
				out = append(out, hangulT+t)
			}
			continue
		}
		if parts, ok := decompositions[c]; ok {
			out = append(out, parts...)
		} else {
			out = append(out, c)
		}
	}
	// A stable insertion sort of each run of nonzero classes, as CPython's nfd_nfkd does it.
	for i := 1; i < len(out); i++ {
		current := CombiningClass(out[i])
		if current == 0 {
			continue
		}
		for o := i - 1; o >= 0; o-- {
			previous := CombiningClass(out[o])
			if previous == 0 || previous <= current {
				break
			}
			out[o], out[o+1] = out[o+1], out[o]
		}
	}
	return out
}

// NFC is NFD followed by CPython's canonical composition (nfc_nfkc in unicodedata.c).
func NFC(s []rune) []rune {
	if quickNFC(s) {
		return append([]rune(nil), s...)
	}
	d := NFD(s)
	out := make([]rune, 0, len(d))
	skipped := make([]bool, len(d))
	for i := 0; i < len(d); {
		if skipped[i] {
			i++
			continue
		}
		code := d[i]
		if code >= hangulL && code < hangulL+hangulLCount && i+1 < len(d) &&
			d[i+1] >= hangulV && d[i+1] < hangulV+hangulVCount {
			code = hangulS + ((code-hangulL)*hangulVCount+(d[i+1]-hangulV))*hangulTCount
			i += 2
			if i < len(d) && d[i] > hangulT && d[i] < hangulT+hangulTCount {
				code += d[i] - hangulT
				i++
			}
			out = append(out, code)
			continue
		}
		if !composeFirst[code] {
			out = append(out, code)
			i++
			continue
		}
		var comb uint8
		for i1 := i + 1; i1 < len(d); i1++ {
			code1 := d[i1]
			comb1 := CombiningClass(code1)
			if comb != 0 {
				if comb1 == 0 {
					break
				}
				if comb >= comb1 {
					continue // blocked
				}
			}
			if result, ok := compositions[[2]rune{code, code1}]; ok {
				code = result
				skipped[i1] = true
				if !composeFirst[code] {
					break
				}
				continue
			}
			if comb1 == 0 {
				break
			}
			comb = comb1
		}
		out = append(out, code)
		i++
	}
	return out
}

// quickNFC is true for text that NFC leaves alone for certain: ASCII and Latin-1 letters with no
// combining marks.
func quickNFC(s []rune) bool {
	for _, c := range s {
		if c >= 0x300 {
			return false
		}
	}
	return true
}

// NFCString is NFC on a string.
func NFCString(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return string(NFC([]rune(s)))
		}
	}
	return s
}
