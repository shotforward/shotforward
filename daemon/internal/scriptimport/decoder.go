package scriptimport

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode/utf8"

	projectapp "github.com/shotforward/shotforward/daemon/internal/application/project"
	"golang.org/x/text/encoding/simplifiedchinese"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

func decodeText(data []byte) (string, error) {
	switch {
	case bytes.HasPrefix(data, utf8BOM):
		data = data[len(utf8BOM):]
		if !utf8.Valid(data) {
			return "", invalidEncoding("UTF-8 BOM is followed by invalid UTF-8")
		}
		return string(data), nil
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}):
		return decodeUTF16(data[2:], textunicode.LittleEndian, binary.LittleEndian)
	case bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		return decodeUTF16(data[2:], textunicode.BigEndian, binary.BigEndian)
	case utf8.Valid(data):
		return string(data), nil
	}

	if littleEndian, ok := detectUTF16Endian(data); ok {
		if littleEndian {
			return decodeUTF16(data, textunicode.LittleEndian, binary.LittleEndian)
		}
		return decodeUTF16(data, textunicode.BigEndian, binary.BigEndian)
	}
	if !validGB18030(data) {
		return "", invalidEncoding("input is neither valid UTF-8, UTF-16, nor GB18030")
	}
	return decodeWith(data, simplifiedchinese.GB18030.NewDecoder())
}

func decodeWith(data []byte, transformer transform.Transformer) (string, error) {
	decoded, _, err := transform.Bytes(transformer, data)
	if err != nil || !utf8.Valid(decoded) {
		if err == nil {
			err = fmt.Errorf("decoder returned invalid UTF-8")
		}
		return "", invalidEncoding(err.Error())
	}
	return string(decoded), nil
}

func decodeUTF16(data []byte, endian textunicode.Endianness, byteOrder binary.ByteOrder) (string, error) {
	if !validUTF16(data, byteOrder) {
		return "", invalidEncoding("UTF-16 input is truncated or contains an invalid surrogate pair")
	}
	return decodeWith(data, textunicode.UTF16(endian, textunicode.IgnoreBOM).NewDecoder())
}

func detectUTF16Endian(data []byte) (bool, bool) {
	if len(data) < 4 || len(data)%2 != 0 {
		return false, false
	}
	sample := data
	if len(sample) > 256 {
		sample = sample[:256]
		if len(sample)%2 != 0 {
			sample = sample[:len(sample)-1]
		}
	}
	var evenZeros, oddZeros int
	for index, value := range sample {
		if value != 0 {
			continue
		}
		if index%2 == 0 {
			evenZeros++
		} else {
			oddZeros++
		}
	}
	pairs := len(sample) / 2
	if oddZeros*3 > pairs*2 && evenZeros*10 < pairs {
		return true, true
	}
	if evenZeros*3 > pairs*2 && oddZeros*10 < pairs {
		return false, true
	}
	return false, false
}

func invalidEncoding(message string) error {
	return fmt.Errorf("%w: %s", projectapp.ErrScriptImportEncoding, message)
}

func validUTF16(data []byte, order binary.ByteOrder) bool {
	if len(data)%2 != 0 {
		return false
	}
	for index := 0; index < len(data); index += 2 {
		value := order.Uint16(data[index : index+2])
		switch {
		case value >= 0xd800 && value <= 0xdbff:
			if index+3 >= len(data) {
				return false
			}
			next := order.Uint16(data[index+2 : index+4])
			if next < 0xdc00 || next > 0xdfff {
				return false
			}
			index += 2
		case value >= 0xdc00 && value <= 0xdfff:
			return false
		}
	}
	return true
}

func validGB18030(data []byte) bool {
	for index := 0; index < len(data); {
		first := data[index]
		if first <= 0x80 {
			index++
			continue
		}
		if first < 0x81 || first > 0xfe || index+1 >= len(data) {
			return false
		}
		second := data[index+1]
		if second >= 0x30 && second <= 0x39 {
			if index+3 >= len(data) || data[index+2] < 0x81 || data[index+2] > 0xfe ||
				data[index+3] < 0x30 || data[index+3] > 0x39 {
				return false
			}
			index += 4
			continue
		}
		if (second < 0x40 || second > 0xfe) || second == 0x7f {
			return false
		}
		index += 2
	}
	return true
}
