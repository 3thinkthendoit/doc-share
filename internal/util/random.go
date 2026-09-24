package util

import "crypto/rand"

const hexChars = "0123456789abcdef"

// RandomHex 生成 n 字节的随机十六进制字符串（长度 2n）
func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	dst := make([]byte, n*2)
	for i, v := range b {
		dst[i*2] = hexChars[v>>4]
		dst[i*2+1] = hexChars[v&0x0f]
	}
	return string(dst)
}

const slugChars = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RandomSlug 生成长度 len 的随机短标识（去掉易混淆字符）
func RandomSlug(length int) string {
	b := make([]byte, length)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = slugChars[int(b[i])%len(slugChars)]
	}
	return string(b)
}
