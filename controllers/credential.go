package controllers

import (
	cryptorand "crypto/rand"
	"math/big"
	"math/rand"
)

const (
	digits   = "0123456789"
	specials = "~=+%^*/()[]{}/!@#$?|"
	all      = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz" + digits + specials
)

func generateUsername(engine string) string {
	if engine == "postgres" {
		return "postgres"
	} else {
		return "admin"
	}
}

func generateDBName(engine string) string {
	//TODO
	return
}

func generatePassword() string {
	length := 12
	buf := make([]byte, length)
	buf[0] = digits[getRandInt(len(digits))]
	buf[1] = specials[getRandInt(len(specials))]
	for i := 2; i < length; i++ {
		buf[i] = all[getRandInt(len(all))]
	}
	rand.Shuffle(len(buf), func(i, j int) {
		buf[i], buf[j] = buf[j], buf[i]
	})
	return string(buf) // E.g. "3i[g0|)z"
}

func getRandInt(s int) int64 {
	result, _ := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(s)))
	return result.Int64()
}
