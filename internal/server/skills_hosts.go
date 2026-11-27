package server

import (
	"strconv"
	"strings"
)

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// cksum is POSIX cksum(1) ("<crc> <size>"), so a copy here and one over there
// (where only the cksum tool is sure to be) can be compared.
func cksum(data []byte) string {
	var crc uint32
	step := func(b byte) {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	for _, b := range data {
		step(b)
	}
	for n := len(data); n > 0; n >>= 8 {
		step(byte(n))
	}
	return strconv.FormatUint(uint64(^crc), 10) + " " + strconv.Itoa(len(data))
}
