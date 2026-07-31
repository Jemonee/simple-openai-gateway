package gateway

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"strings"
)

const (
	compressedPayloadPrefix     = "gzip:v1:"
	payloadCompressionThreshold = 2 << 10
)

func compressStoredPayload(data []byte) string {
	if len(data) < payloadCompressionThreshold || bytes.HasPrefix(data, []byte(compressedPayloadPrefix)) {
		return string(data)
	}
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil || writer.Close() != nil {
		return string(data)
	}
	encoded := compressedPayloadPrefix + base64.RawStdEncoding.EncodeToString(buffer.Bytes())
	if len(encoded) >= len(data) {
		return string(data)
	}
	return encoded
}

func decompressStoredPayload(value string) string {
	decompressed, ok := decodeStoredPayload(value)
	if !ok {
		return value
	}
	return decompressed
}

func decodeStoredPayload(value string) (string, bool) {
	if !strings.HasPrefix(value, compressedPayloadPrefix) {
		return value, true
	}
	encoded := strings.TrimPrefix(value, compressedPayloadPrefix)
	compressed, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", false
	}
	decompressed, err := io.ReadAll(io.LimitReader(reader, maxDetailedPayloadBytes+1))
	closeErr := reader.Close()
	if err != nil || closeErr != nil || len(decompressed) > maxDetailedPayloadBytes {
		return "", false
	}
	return string(decompressed), true
}
